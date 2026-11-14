package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"commandcode-go-cliproxyapi/internal/catalog"
	"commandcode-go-cliproxyapi/internal/config"
)

const (
	cooledRefreshKey  = "sk-refresh-cooled-key"
	goodRefreshKey    = "sk-refresh-good-key"
	refreshTwoKeyYAML = "api-keys:\n" +
		"  - value: " + cooledRefreshKey + "\n" +
		"  - value: " + goodRefreshKey + "\n" +
		"catalog-url: https://catalog.test/models\n"
)

// authRecorder answers host.http.do, records every Authorization header, and
// rejects the cooled first key 401-style while any other key gets a good
// catalog body.
type authRecorder struct {
	mu    sync.Mutex
	auths []string
	logs  [][]byte
	// body, when non-empty, replaces testCatalogJSON as the served catalog.
	body []byte
	// rejectAll, when true, rejects every key 401-style regardless of value.
	rejectAll bool
}

func (r *authRecorder) call(method string, payload []byte) ([]byte, error) {
	if method == pluginabi.MethodHostLog {
		r.mu.Lock()
		r.logs = append(r.logs, append([]byte(nil), payload...))
		r.mu.Unlock()
		return hostOK(map[string]any{}), nil
	}
	if method != pluginabi.MethodHostHTTPDo {
		return hostOK(map[string]any{}), nil
	}
	var wire struct {
		Headers http.Header `json:"headers"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return hostErr("test", "undecodable payload"), nil
	}
	auth := wire.Headers.Get("Authorization")
	r.mu.Lock()
	r.auths = append(r.auths, auth)
	reject := r.rejectAll
	r.mu.Unlock()
	if reject || auth == "Bearer "+cooledRefreshKey {
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized}), nil
	}
	body := testCatalogJSON
	if len(r.body) > 0 {
		body = string(r.body)
	}
	return hostOK(pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(body),
	}), nil
}

func (r *authRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auths...)
}

func (r *authRecorder) logCalls() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.logs...)
}

func twoKeyFixture(t *testing.T) (config.Config, *catalog.Manager, *HostBridge, *authRecorder) {
	t.Helper()
	cfg, err := config.Load([]byte(refreshTwoKeyYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rec := &authRecorder{}
	bridge := NewHostBridge(rec.call)
	return cfg, catalog.New(cfg, bridge), bridge, rec
}

// TestRefreshFallsThroughWithinRefreshOnce pins the Phase 5 fallback as
// failover: catalog refresh tries the first configured key, and when that
// key fails it tries the next configured key before giving up (a pinned
// first-key-only refresh left the model catalog stale forever once the
// first key died, even with healthy keys remaining).
func TestRefreshFallsThroughWithinRefreshOnce(t *testing.T) {
	cfg, mgr, bridge, rec := twoKeyFixture(t)
	if err := refreshOnce(context.Background(), mgr, bridge, time.Second, cfg); err != nil {
		t.Fatalf("refreshOnce with one dead key: %v", err)
	}
	auths := rec.seen()
	want := []string{"Bearer " + cooledRefreshKey, "Bearer " + goodRefreshKey}
	if len(auths) != 2 || auths[0] != want[0] || auths[1] != want[1] {
		t.Fatalf("refresh auths = %v; want failover %v", auths, want)
	}
	if len(mgr.Models()) == 0 {
		t.Fatal("catalog stayed empty after failover to the healthy key")
	}
	fallthroughWarns := 0
	for _, blob := range rec.logCalls() {
		if strings.Contains(string(blob), "trying next configured key") {
			fallthroughWarns++
		}
	}
	if fallthroughWarns != 1 {
		t.Fatalf("failover warn count = %d, want 1", fallthroughWarns)
	}
}

// TestRefreshAllKeysFailStillErrors pins that refreshOnce reports an error
// when every configured key fails — failover must not mask a total outage.
func TestRefreshAllKeysFailStillErrors(t *testing.T) {
	cfg, mgr, bridge, rec := twoKeyFixture(t)
	rec.mu.Lock()
	rec.rejectAll = true
	rec.mu.Unlock()
	if err := refreshOnce(context.Background(), mgr, bridge, time.Second, cfg); err == nil {
		t.Fatal("expected error when every configured key fails")
	}
	if got := rec.seen(); len(got) != 2 {
		t.Fatalf("refresh attempts = %v, want one per configured key", got)
	}
}

// TestRefreshUnsupportedLogQuotesNewlineIDs pins F2: a catalog entry excluded
// from the routable set must reach the host.log "models" field quoted (%q), so
// no raw newline carried by an upstream ID can forge log lines (CWE-117).
// The exclusion is produced by the protocol kill-switch — the default route is
// chat-completions here, so turning that switch off demotes the entry while
// leaving its ID in the diagnostic.
func TestRefreshUnsupportedLogQuotesNewlineIDs(t *testing.T) {
	const evilID = "evil\n2027-01-01 ERROR forged host.log line"
	cfg, err := config.Load([]byte("api-keys:\n  - value: sk-refresh-evil-key\n" +
		"catalog-url: https://catalog.test/models\n" +
		"protocols:\n  chat-completions: false\n"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rec := &authRecorder{body: []byte(`{"data":[{"id":"evil\n2027-01-01 ERROR forged host.log line"}]}`)}
	bridge := NewHostBridge(rec.call)
	mgr := catalog.New(cfg, bridge)
	if err := refreshOnce(context.Background(), mgr, bridge, time.Second, cfg); err != nil {
		t.Fatalf("refreshOnce: %v", err)
	}
	logCalls := rec.logCalls()
	if len(logCalls) != 1 {
		t.Fatalf("host.log calls = %d, want exactly one unsupported-models warn", len(logCalls))
	}
	var req struct {
		Level   string         `json:"level"`
		Message string         `json:"message"`
		Fields  map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(logCalls[0], &req); err != nil {
		t.Fatalf("decode host.log payload: %v", err)
	}
	if req.Message != "unsupported models excluded from routable catalog" {
		t.Fatalf("unexpected log message: %q", req.Message)
	}
	modelsField, _ := req.Fields["models"].(string)
	if strings.ContainsRune(modelsField, '\n') {
		t.Fatalf("models log field contains raw newline (log injection): %q", modelsField)
	}
	if want := fmt.Sprintf("%q: ", evilID); !strings.Contains(modelsField, want) {
		t.Fatalf("models field must quote the upstream ID; got %q, want prefix %q", modelsField, want)
	}
}

// TestRefreshOnceNilBridgeSurvivesDiagnostics pins the nil-bridge contract:
// handleLifecycle deliberately supports a bridge-less Manager, so
// refreshOnce's post-refresh diagnostics (unsupported models, normalization
// warnings) must tolerate a nil bridge exactly like its failure-log paths —
// an unguarded bridge.Log here turns a SUCCESSFUL register/reconfigure into
// a plugin_error via the recover in HandleCall.
func TestRefreshOnceNilBridgeSurvivesDiagnostics(t *testing.T) {
	const cfgYAML = "api-keys:\n  - value: " + goodRefreshKey + "\n" +
		"catalog-url: https://catalog.test/models\n" +
		"protocols:\n  chat-completions: false\n"
	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	mgr := catalog.New(cfg, NewHostBridge((&authRecorder{}).call))
	var panicked bool
	func() {
		defer func() { panicked = recover() != nil }()
		err = refreshOnce(context.Background(), mgr, nil, time.Second, cfg)
	}()
	if panicked {
		t.Fatalf("refreshOnce panicked on the nil-bridge diagnostics path (err=%v)", err)
	}
	if err != nil {
		t.Fatalf("refresh with diagnostics: %v", err)
	}
}
