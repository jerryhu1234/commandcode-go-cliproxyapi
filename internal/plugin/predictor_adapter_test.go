package plugin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"commandcode-go-cliproxyapi/internal/config"
	"commandcode-go-cliproxyapi/internal/predictor"
)

// accountResponder answers host.http.do by route so one fake covers the
// credits → subscriptions fan-out of fetchPredictorSnapshot.
func accountResponder(creditsBody, subBody string) func(string, []byte) ([]byte, error) {
	return func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire map[string]any
		if err := json.Unmarshal(payload, &wire); err != nil {
			return hostErr("bad_request", "malformed bridge payload"), nil
		}
		url, _ := wire["url"].(string)
		answer := func(body string) []byte {
			return hostOK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Headers:    http.Header{"Content-Type": []string{"application/json"}},
				Body:       []byte(body),
			})
		}
		switch {
		case strings.Contains(url, "/alpha/billing/credits"):
			return answer(creditsBody), nil
		case strings.Contains(url, "/alpha/billing/subscriptions"):
			return answer(subBody), nil
		default:
			return hostErr("upstream_down", "unexpected predictor URL "+url), nil
		}
	}
}

const (
	testCreditsBody = `{"credits":{"belowThreshold":false,"creditThreshold":0,` +
		`"monthlyCredits":30,"purchasedCredits":12,"freeCredits":0},` +
		`"windowLimits":{"limited":true,` +
		`"fiveHour":{"used":6,"cap":24,"exceeded":false,"resetAt":1798329600000},` +
		`"weekly":{"used":56,"cap":56,"exceeded":true,"resetAt":1798934400000}}}`
	testSubscriptionBody = `{"success":true,"data":{"planId":"individual-pro-v1",` +
		`"status":"active","currentPeriodEnd":"2026-11-01T04:05:06.000Z"}}`
)

// predictorIdentityResponder answers the auth-file email lookup plus the
// credits/subscriptions/whoami fan-out used by the predictor label fallback.
func predictorIdentityResponder(email, whoami string) func(string, []byte) ([]byte, error) {
	return func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthList {
			files := []pluginapi.HostAuthFileEntry{}
			if email != "" {
				id, _ := quotaIdentity(testKey)
				files = append(files, pluginapi.HostAuthFileEntry{
					ID: id + ".json", Name: id + ".json", Email: email, Source: "file",
				})
			}
			return hostOK(hostAuthListResponse{Files: files}), nil
		}
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var wire map[string]any
		if err := json.Unmarshal(payload, &wire); err != nil {
			return hostErr("bad_request", "malformed bridge payload"), nil
		}
		url, _ := wire["url"].(string)
		body := ""
		switch {
		case strings.Contains(url, "/alpha/billing/credits"):
			body = testCreditsBody
		case strings.Contains(url, "/alpha/billing/subscriptions"):
			body = testSubscriptionBody
		case strings.Contains(url, "/alpha/whoami"):
			body = whoami
		default:
			return hostErr("upstream_down", "unexpected predictor URL "+url), nil
		}
		return hostOK(pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(body),
		}), nil
	}
}

// TestFetchPredictorSnapshotUsesStoredEmailAsLabel pins the shared identity
// contract: the predictor labels an account with the same mailbox the quota
// page already synced into its auth file, without spending an upstream call.
func TestFetchPredictorSnapshotUsesStoredEmailAsLabel(t *testing.T) {
	const email = "predictor@example.test"
	m, f := newTestManager(predictorIdentityResponder(email, ""))
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	snap, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Label != email {
		t.Fatalf("label = %q, want the stored account email %q", snap.Label, email)
	}
	for _, call := range f.callsOf(pluginabi.MethodHostHTTPDo) {
		var wire hostHTTPReq
		_ = json.Unmarshal(call.payload, &wire)
		if strings.Contains(wire.URL, "/alpha/whoami") {
			t.Fatalf("stored email must not trigger a whoami call: %s", wire.URL)
		}
	}
}

// TestFetchPredictorSnapshotFallsBackToWhoamiIdentity covers accounts whose
// email was never synced: the label falls back to the account identity the
// quota page uses, preferring email, then the handle/display name, then org.
func TestFetchPredictorSnapshotFallsBackToWhoamiIdentity(t *testing.T) {
	m, _ := newTestManager(predictorIdentityResponder("", `{"success":true,"user":{"name":"Test User","userName":"test-user"},"org":null}`))
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	snap, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Label != "test-user" {
		t.Fatalf("label = %q, want the account handle", snap.Label)
	}
}

// TestFetchPredictorSnapshotReadsWhoamiDataWrapper keeps the identity parser
// robust to the response envelope changing between CommandCode releases.
func TestFetchPredictorSnapshotReadsWhoamiDataWrapper(t *testing.T) {
	m, _ := newTestManager(predictorIdentityResponder("", `{"data":{"user":{"userName":"wrapped"},"org":{"login":"team"}}}`))
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	snap, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Label != "team (wrapped)" {
		t.Fatalf("label = %q, want the wrapped org/account identity", snap.Label)
	}
}

// TestBuildPredictorSeedsAccountEmailLabels keeps the prediction page from
// showing the digest placeholder between an engine start and its first sample:
// the mailbox already present in the auth file becomes the initial key label.
func TestBuildPredictorSeedsAccountEmailLabels(t *testing.T) {
	const email = "seeded@example.test"
	m, _ := newTestManager(predictorIdentityResponder(email, ""))
	cfg := mustTestConfig(t)

	engine := m.buildPredictor(context.Background(), cfg)
	if engine == nil {
		t.Fatal("predictor engine disabled")
	}
	snap := engine.Snapshot(nil)
	if len(snap.Accounts) != 1 || snap.Accounts[0].Label != email {
		t.Fatalf("accounts = %+v, want the seeded email label", snap.Accounts)
	}
}

// TestFetchPredictorSnapshotMapping pins the D12 mapping: the monthly plan
// window (remaining/cap/period-end) lands in Included, the rolling
// rate-limit windows become percent meters (exceeded → 0), purchased stays
// a billing channel, and no hourly buckets are invented (flat profile).
func TestFetchPredictorSnapshotMapping(t *testing.T) {
	m, _ := newTestManager(accountResponder(testCreditsBody, testSubscriptionBody))
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	snap, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	id, label := quotaIdentity(testKey)
	if snap.KeyID != id || snap.Label != label {
		t.Fatalf("identity = %s/%s, want %s/%s", snap.KeyID, snap.Label, id, label)
	}
	if snap.Included == nil || snap.Included.Remaining != 30 || snap.Included.Cap != 80 {
		t.Fatalf("included = %+v, want remaining 30 capped at the Pro v1 allowance 80", snap.Included)
	}
	if got, want := snap.Included.ResetsAt, "2026-11-01T04:05:06Z"; got.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("resets = %v, want %s", got, want)
	}
	if snap.Session == nil || math.Abs(snap.Session.RemainingPercent-75) > 1e-9 {
		t.Fatalf("session = %+v, want 75 (used 6 of 24)", snap.Session)
	}
	if snap.Weekly == nil || snap.Weekly.RemainingPercent != 0 {
		t.Fatalf("weekly = %+v, want 0 (exceeded)", snap.Weekly)
	}
	if snap.PurchasedUSD != 12 {
		t.Fatalf("purchased = %v, want 12", snap.PurchasedUSD)
	}
	if len(snap.Hours) != 0 {
		t.Fatalf("hours = %+v, want none (no bucket API on the account surface)", snap.Hours)
	}
}

// TestFetchPredictorSnapshotUnknownPlan: without a recognizable plan the
// monthly window is omitted (no denominator, no bar) but the percent gates
// still land so the predictor keeps gating.
func TestFetchPredictorSnapshotUnknownPlan(t *testing.T) {
	credits := `{"credits":{"monthlyCredits":30,"purchasedCredits":0},` +
		`"windowLimits":{"limited":true,` +
		`"fiveHour":{"used":0,"cap":0,"exceeded":false,"resetAt":0},` +
		`"weekly":{"used":0,"cap":56,"exceeded":false,"resetAt":0}}}`
	m, _ := newTestManager(accountResponder(credits, `{"success":false}`))
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	snap, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Included != nil {
		t.Fatalf("included = %+v, want nil for an unknown plan", snap.Included)
	}
	if snap.Session != nil {
		t.Fatalf("session = %+v, want nil for an uncapped inactive gate", snap.Session)
	}
	if snap.Weekly == nil || snap.Weekly.RemainingPercent != 100 {
		t.Fatalf("weekly = %+v, want 100 (unused)", snap.Weekly)
	}
	if snap.PurchasedUSD != 0 {
		t.Fatalf("purchased = %v, want 0", snap.PurchasedUSD)
	}
}

// TestFetchPredictorSnapshotRejects429 pins the backoff contract: an
// upstream 429 surfaces as predictor.RetryAfter so the engine parks the key.
func TestFetchPredictorSnapshotRejects429(t *testing.T) {
	m, _ := newTestManager(func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		return hostOK(pluginapi.HTTPResponse{
			StatusCode: http.StatusTooManyRequests,
			Headers:    http.Header{"Retry-After": []string{"7"}},
			Body:       []byte(`{"error":"slow down"}`),
		}), nil
	})
	m.mu.Lock()
	m.cfg = mustTestConfig(t)
	m.mu.Unlock()

	_, err := m.fetchPredictorSnapshot(context.Background(), testKey)
	if err == nil {
		t.Fatal("want a rejection error")
	}
	if d, ok := predictor.RetrySeconds(err); !ok || d != 7*time.Second {
		t.Fatalf("retry hint = (%v, %t), want 7s", d, ok)
	}
}

// mustTestConfig builds the minimal config the adapter reads (base-url only).
func mustTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load([]byte(testValidYAML))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
