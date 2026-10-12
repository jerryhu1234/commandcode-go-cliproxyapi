package plugin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

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
