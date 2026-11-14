package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// The upstream key arrives by auth file, not config: the host routes
// requests to any materialized auth entry, so a config-removed key's file
// keeps being a ROUTEABLE credential until an operator deletes it by hand.
// These tests pin the L0 orphan-detection diagnostic: every lifecycle
// (register/reconfigure) reports config-removed CommandCode auth files by
// digest through host.log so the operator sees the dead-credential surface
// before it turns into routing noise.

func registerWithAuthFiles(t *testing.T, authFiles map[string]struct{}) ([]byte, bool) {
	t.Helper()
	f := &fakeCaller{responder: catalogResponder(true, testCatalogJSON), authFiles: map[string]struct{}{}}
	for name := range authFiles {
		f.authFiles[name] = struct{}{}
	}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	resp, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	env := decodeEnv(t, resp)
	var orphans []byte
	for _, c := range f.callsOf(pluginabi.MethodHostLog) {
		var wire struct {
			Level   string         `json:"level"`
			Message string         `json:"message"`
			Fields  map[string]any `json:"fields"`
		}
		if json.Unmarshal(c.payload, &wire) != nil {
			continue
		}
		if strings.Contains(strings.ToLower(wire.Message), "orphan") {
			raw, _ := json.Marshal(wire.Fields)
			orphans = raw
		}
	}
	return []byte(orphans), env.OK
}

// TestReconfigureReportsOrphanedAuthFiles: a stale CommandCode auth file
// whose key is no longer in the config must be reported by digest; the
// still-configured key and foreign-provider files must not be reported.
func TestReconfigureReportsOrphanedAuthFiles(t *testing.T) {
	staleHash := authKeyHash("sk-removed-long-ago")
	files := map[string]struct{}{
		authFileNameFromHash(authKeyHash(testKey)): {}, // current config key
		authFileNameFromHash(staleHash):            {}, // removed from config
		"someother-provider.json":                  {}, // foreign provider
	}
	orphans, ok := registerWithAuthFiles(t, files)
	if !ok {
		t.Fatal("register failed")
	}
	if len(orphans) == 0 {
		t.Fatal("orphaned auth file was not reported through host.log")
	}
	if !strings.Contains(string(orphans), staleHash) {
		t.Fatalf("report %q does not name the stale digest", orphans)
	}
	if strings.Contains(string(orphans), authKeyHash(testKey)) {
		t.Fatalf("report %q includes a still-configured key digest", orphans)
	}
}

// TestReconfigureWithoutOrphansKeepsSilent: with every materialized
// CommandCode auth file matching the config, no orphan warning fires —
// the diagnostic must stay reserved for actual incidents.
func TestReconfigureWithoutOrphansKeepsSilent(t *testing.T) {
	files := map[string]struct{}{
		authFileNameFromHash(authKeyHash(testKey)): {},
		"someother-provider.json":                  {},
	}
	orphans, ok := registerWithAuthFiles(t, files)
	if !ok {
		t.Fatal("register failed")
	}
	if len(orphans) != 0 {
		t.Fatalf("no orphans exist but a warning was logged: %s", orphans)
	}
}
