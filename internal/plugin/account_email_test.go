package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// emailSyncFixture registers the plugin against a fake host whose AuthList
// answers with one file-backed credential for testKey, physically present in
// a temp dir so syncAccountEmail can exercise its real read-merge-rename path.
func emailSyncFixture(t *testing.T, authPath string) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthList {
			return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{{
				Name: authFileNameFromHash(authKeyHash(testKey)),
				ID:   authRecordIDFromHash(authKeyHash(testKey)),
				Path: authPath,
			}}}), nil
		}
		return hostOK(map[string]any{}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	return m, f
}

// TestSyncAccountEmailPreservesExistingFields pins read-merge-write shape:
// the email is the ONLY field that changes — api_key, label, id, disabled,
// and any host-managed metadata survive byte-for-byte, or the host loses the
// credential identity on its watcher reload.
func TestSyncAccountEmailPreservesExistingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, authFileNameFromHash(authKeyHash(testKey)))
	before := `{"type":"commandcode","id":"cc-1","label":"CommandCode credential","api_key":"` + testKey + `","disabled":false,"storage":{"extra":1}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := emailSyncFixture(t, path)
	if err := m.syncAccountEmail(context.Background(), testKey, "ops@example.com"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("auth file no longer valid json: %v (%s)", err, raw)
	}
	if after["email"] != "ops@example.com" {
		t.Errorf("email = %v", after["email"])
	}
	for _, tc := range []struct{ field, want string }{
		{"type", "commandcode"}, {"id", "cc-1"}, {"label", "CommandCode credential"}, {"api_key", testKey},
	} {
		if got, _ := after[tc.field].(string); got != tc.want {
			t.Errorf("field %s = %v, want %q", tc.field, after[tc.field], tc.want)
		}
	}
	if after["disabled"] != false || after["storage"] == nil {
		t.Errorf("host-managed fields lost: %v", after)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("sync left a stray temp file: %s", e.Name())
		}
	}
}

// TestSyncAccountEmailConcurrentWriters pins atomic replacement: overlapping
// syncs (two refresh ticks racing on the same credential) must always leave a
// fully valid auth file holding one of the racing emails — never a torn or
// emptied file — and must not litter the auth dir with temp files.
func TestSyncAccountEmailConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, authFileNameFromHash(authKeyHash(testKey)))
	if err := os.WriteFile(path, []byte(`{"type":"commandcode","api_key":"`+testKey+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := emailSyncFixture(t, path)
	const writers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		mail := "writer" + string(rune('a'+i)) + "@example.com"
		wg.Add(1)
		go func(mail string) {
			defer wg.Done()
			<-start
			errs <- m.syncAccountEmail(context.Background(), testKey, mail)
		}(mail)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sync failed: %v", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("torn auth file after concurrent sync: %v (%s)", err, raw)
	}
	email, _ := after["email"].(string)
	if len(email) < len("a@example.com") || email[len(email)-len("@example.com"):] != "@example.com" {
		t.Errorf("final email %q is not one of the racing writers' values", email)
	}
	if after["api_key"] != testKey {
		t.Errorf("api_key lost under concurrency: %v", after["api_key"])
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("auth dir littered with temp files: %v", names)
	}
}
