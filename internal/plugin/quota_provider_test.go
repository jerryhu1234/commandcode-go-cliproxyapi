package plugin

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"commandcode-go-cliproxyapi/internal/config"
)

const quotaProviderSecret = "sk-native-quota-secret"

func quotaProviderFixture(t *testing.T) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var req hostHTTPReq
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Fatalf("decode host request: %v", err)
		}
		if req.HostCallbackID != "quota-callback" {
			t.Fatalf("host_callback_id = %q", req.HostCallbackID)
		}
		if got := req.Headers.Get("Authorization"); got != "Bearer "+quotaProviderSecret {
			t.Fatalf("authorization header was not derived from credential context")
		}
		var body string
		switch {
		case strings.HasSuffix(req.URL, accountCreditsPath):
			body = `{"credits":{"monthlyCredits":4,"purchasedCredits":1},"windowLimits":{"limited":true,"fiveHour":{"used":10,"cap":10,"exceeded":true,"resetAt":1893456000000},"weekly":{"used":2,"cap":8,"resetAt":1893542400000}}}`
		case strings.HasSuffix(req.URL, accountSubscriptionPath):
			body = `{"success":true,"data":{"planId":"individual-go","currentPeriodEnd":"2030-01-03T00:00:00Z"}}`
		case strings.Contains(req.URL, "/alpha/whoami"):
			body = `{"success":true,"user":{"email":"quota@example.test"}}`
		default:
			t.Fatalf("unexpected quota URL %q", req.URL)
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}), nil
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg = config.Config{BaseURL: "https://api.example.test/provider/v1", RequestTimeout: time.Second}
	return m, f
}

func TestQuotaProviderDispatchAndCredentialContext(t *testing.T) {
	m, f := quotaProviderFixture(t)

	raw, err := m.HandleCall(pluginabi.MethodQuotaIdentifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	var identifier struct {
		Identifier string `json:"identifier"`
	}
	decodeResult(t, raw, &identifier)
	if identifier.Identifier != ProviderID {
		t.Fatalf("identifier = %q", identifier.Identifier)
	}

	raw, err = m.HandleCall(pluginabi.MethodQuotaDescribe, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var description pluginapi.QuotaDescribeResponse
	decodeResult(t, raw, &description)
	if description.SupportsReset || len(description.SupportedProviders) != 1 || description.SupportedProviders[0] != ProviderID {
		t.Fatalf("description = %+v", description)
	}

	req := struct {
		pluginapi.QuotaFetchRequest
		HostCallbackID string `json:"host_callback_id"`
	}{
		QuotaFetchRequest: pluginapi.QuotaFetchRequest{
			AuthIndex: "index-not-config-order", AuthID: "credential-id", Provider: ProviderID,
			Attributes:  map[string]string{"api_key": quotaProviderSecret},
			StorageJSON: []byte(`{"type":"commandcode","api_key":"` + quotaProviderSecret + `"}`),
		},
		HostCallbackID: "quota-callback",
	}
	raw, err = m.HandleCall(pluginabi.MethodQuotaFetch, mustJSON(req))
	if err != nil {
		t.Fatal(err)
	}
	var got pluginapi.QuotaFetchResponse
	decodeResult(t, raw, &got)
	if got.Subscription == nil || got.Subscription.Plan != "Go" || len(got.Groups) != 1 || len(got.Groups[0].Buckets) != 3 {
		t.Fatalf("native quota = %+v", got)
	}
	if fraction := got.Groups[0].Buckets[1].RemainingFraction; fraction != 0 {
		t.Fatalf("exhausted five-hour remaining fraction = %v", fraction)
	}
	if fraction := got.Groups[0].Buckets[2].RemainingFraction; math.Abs(fraction-.75) > 1e-9 {
		t.Fatalf("weekly remaining fraction = %v", fraction)
	}
	for _, call := range f.callsOf(pluginabi.MethodHostHTTPDo) {
		var wire hostHTTPReq
		if json.Unmarshal(call.payload, &wire) != nil {
			t.Fatalf("invalid callback payload")
		}
		wire.Headers.Del("Authorization")
		redacted, _ := json.Marshal(wire)
		if strings.Contains(string(redacted), quotaProviderSecret) {
			t.Fatal("credential leaked outside authorization header")
		}
	}
}

func TestQuotaProviderStorageFallbackAndLegacySemantics(t *testing.T) {
	m, _ := quotaProviderFixture(t)
	p := newQuotaProvider(m, "quota-callback")
	got, err := p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: ProviderID, StorageJSON: []byte(`{"type":"commandcode","api_key":"` + quotaProviderSecret + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy, _, _, err := fetchQuota(context.Background(), m.bridge.withHostCallbackID("quota-callback"), m.cfg.BaseURL, m.cfg.RequestTimeout, quotaProviderSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := nativeQuotaResponse(legacy)
	if string(mustJSON(got)) != string(mustJSON(want)) {
		t.Fatalf("native and legacy normalization differ:\n got %s\nwant %s", mustJSON(got), mustJSON(want))
	}
}

func TestQuotaProviderValidationUnknownsAndReset(t *testing.T) {
	m, f := quotaProviderFixture(t)
	p := newQuotaProvider(m, "quota-callback")
	for name, req := range map[string]pluginapi.QuotaFetchRequest{
		"provider mismatch": {Provider: "other", Attributes: map[string]string{"api_key": quotaProviderSecret}, StorageJSON: []byte(`{"type":"commandcode","api_key":"` + quotaProviderSecret + `"}`)},
		"attributes only":   {Provider: ProviderID, Attributes: map[string]string{"api_key": quotaProviderSecret}},
		"malformed storage": {Provider: ProviderID, StorageJSON: []byte(`{"api_key":`)},
		"other type":        {Provider: ProviderID, Attributes: map[string]string{"api_key": quotaProviderSecret}, StorageJSON: []byte(`{"type":"other","api_key":"` + quotaProviderSecret + `"}`)},
		"other provider":    {Provider: ProviderID, Attributes: map[string]string{"api_key": quotaProviderSecret}, StorageJSON: []byte(`{"provider":"other","api_key":"` + quotaProviderSecret + `"}`)},
		"missing identity":  {Provider: ProviderID, StorageJSON: []byte(`{"api_key":"` + quotaProviderSecret + `"}`)},
		"conflicting IDs":   {Provider: ProviderID, StorageJSON: []byte(`{"type":"commandcode","provider":"other","api_key":"` + quotaProviderSecret + `"}`)},
		"key conflict":      {Provider: ProviderID, Attributes: map[string]string{"api_key": quotaProviderSecret}, StorageJSON: []byte(`{"type":"commandcode","api_key":"different-secret"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.FetchQuota(context.Background(), req); err == nil || strings.Contains(err.Error(), quotaProviderSecret) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if got := len(f.callsOf(pluginabi.MethodHostHTTPDo)); got != 0 {
		t.Fatalf("invalid credentials made HTTP calls: %d", got)
	}
	before := len(f.callsOf(pluginabi.MethodHostHTTPDo))
	if _, err := p.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: ProviderID, Attributes: map[string]string{"api_key": quotaProviderSecret}}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("reset error = %v", err)
	}
	if after := len(f.callsOf(pluginabi.MethodHostHTTPDo)); after != before {
		t.Fatalf("reset made HTTP calls: before=%d after=%d", before, after)
	}

	unknown := nativeQuotaResponse(quotaUsage{FiveHour: quotaWindow{Status: "ok"}, Weekly: quotaWindow{Status: "unlimited"}})
	if unknown.Subscription != nil || len(unknown.Groups) != 1 || len(unknown.Groups[0].Buckets) != 1 || unknown.Groups[0].Buckets[0].RemainingFraction != 1 {
		t.Fatalf("unknown/unlimited normalization = %+v", unknown)
	}

	raw, err := m.HandleCall(pluginabi.MethodQuotaFetch, []byte(`{"provider":`))
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || env.OK || env.Error == nil || env.Error.Code != "invalid_request" {
		t.Fatalf("malformed envelope = %s", raw)
	}
}

func TestQuotaProviderFetchesTrustedStorageByAuthIndex(t *testing.T) {
	m, f := quotaProviderFixture(t)
	f.responder = func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthGet {
			var req struct {
				AuthIndex      string `json:"auth_index"`
				HostCallbackID string `json:"host_callback_id"`
			}
			if json.Unmarshal(payload, &req) != nil || req.AuthIndex != "trusted-index" || req.HostCallbackID != "quota-callback" {
				t.Fatalf("auth get request = %s", payload)
			}
			return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: req.AuthIndex, JSON: json.RawMessage(`{"provider":"commandcode","api_key":"` + quotaProviderSecret + `"}`)}), nil
		}
		if method == pluginabi.MethodHostHTTPDo {
			var req hostHTTPReq
			_ = json.Unmarshal(payload, &req)
			body := `{"credits":{"monthlyCredits":1},"windowLimits":{"limited":false}}`
			return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}), nil
		}
		return hostOK(map[string]any{}), nil
	}
	_, err := newQuotaProvider(m, "quota-callback").FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: ProviderID, AuthIndex: "trusted-index", Attributes: map[string]string{"api_key": quotaProviderSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
}
