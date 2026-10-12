// Predictor adapter: maps the CommandCode account surface (billing credits +
// subscriptions) into predictor.AccountSnapshot values. Ported from the
// ollama-cliproxyapi plugin, whose predictor package is provider-agnostic;
// only this file is CommandCode-specific. Sits in the plugin package so it
// can reuse the account shapes (quota.go) and the digest identity helpers.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"commandcode-go-cliproxyapi/internal/predictor"
)

// accountGet performs one authenticated account-API GET via the host bridge,
// bounded by quotaMaxTimeout like the interactive quota path. An upstream 429
// is annotated with the Retry-After hint so the predictor engine backs off
// exactly as long as the account surface demands.
func (m *Manager) accountGet(ctx context.Context, base, key, path string) ([]byte, error) {
	if m.bridge == nil {
		return nil, fmt.Errorf("account bridge unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, quotaMaxTimeout)
	defer cancel()
	resp, errDo := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    base + path,
		Headers: http.Header{
			"Authorization": []string{"Bearer " + key},
			"Accept":        []string{"application/json"},
		},
	})
	if errDo != nil {
		return nil, fmt.Errorf("account request failed")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, predictor.RetryAfter(
			fmt.Errorf("account request rejected (%d)", resp.StatusCode),
			retryAfterSeconds(resp.Headers),
		)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("account request rejected (%d)", resp.StatusCode)
	}
	return resp.Body, nil
}

// retryAfterFloor keeps a missing or misreported Retry-After meaningful: the
// account surface is interactive-grade, so a minute of cooling down is a
// sane worst-case wait.
const retryAfterFloor = 60 * time.Second

// retryAfterSeconds parses the Retry-After header (delta-seconds form) with
// a 60s floor and a 1h ceiling so an upstream bug cannot stall sampling.
func retryAfterSeconds(h http.Header) time.Duration {
	if h == nil {
		return retryAfterFloor
	}
	raw := strings.TrimSpace(h.Get("Retry-After"))
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return retryAfterFloor
	}
	if secs > 3600 {
		secs = 3600
	}
	return time.Duration(secs) * time.Second
}

// fetchPredictorSnapshot observes one credential's account state (predictor
// FetchFn): remaining plan credits and the rolling windows come from
// /alpha/billing/credits, the monthly allowance and its reset instant from
// /alpha/billing/subscriptions (best-effort). CommandCode exposes no hourly
// usage buckets, so Hours stays empty and the model runs flat-rate — the
// percent windows still gate, and the included series still drives the ETA.
//
// Mapping (D12, mirrors the quota page's own semantics):
//   - Included: the monthly plan window — Remaining = monthlyCredits, Cap =
//     max(allowance, monthlyCredits), ResetsAt = currentPeriodEnd. Omitted
//     when the plan is unknown, because a bar without a denominator would be
//     a lie (same policy as quota page's monthWindow); the account renders
//     dark until its plan is identifiable.
//   - Session/Weekly: the 5h/weekly rate-limit windows as percent meters —
//     exceeded → 0%, capped → (1 - used/cap) clamped to [0,100]; a window
//     with no cap (unlimited gate) maps to nil: absent beats a fabricated
//     full window that never drains.
//   - PurchasedUSD: purchasedCredits as-is (billing channel, never a gate).
func (m *Manager) fetchPredictorSnapshot(ctx context.Context, key string) (predictor.AccountSnapshot, error) {
	m.mu.RLock()
	baseURL := m.cfg.BaseURL
	m.mu.RUnlock()
	base, err := AccountAPIBase(baseURL)
	if err != nil {
		return predictor.AccountSnapshot{}, err
	}
	id, label := quotaIdentity(key)
	snap := predictor.AccountSnapshot{
		KeyID: id, Label: label, At: time.Now().UTC(), PurchasedUSD: -1,
	}
	rawCredits, err := m.accountGet(ctx, base, key, accountCreditsPath)
	if err != nil {
		return predictor.AccountSnapshot{}, err
	}
	var credits accountCredits
	if err := json.Unmarshal(rawCredits, &credits); err != nil {
		// Carry the decoder's own reason: it names the offending field, which
		// is the difference between a one-minute and a one-hour diagnosis.
		return predictor.AccountSnapshot{}, fmt.Errorf("account credits response invalid: %v", err)
	}
	snap.PurchasedUSD = credits.Credits.PurchasedCredits

	allowance := 0.0
	periodEnd := time.Time{}
	if rawSub, errSub := m.accountGet(ctx, base, key, accountSubscriptionPath); errSub == nil {
		var sub accountSubscription
		if json.Unmarshal(rawSub, &sub) == nil && sub.Success {
			_, allowance = planFor(sub.Data.PlanID)
			periodEnd = parseRFC3339Time(sub.Data.CurrentPeriodEnd)
		}
	}
	if allowance > 0 {
		snap.Included = &predictor.USDWindow{
			Remaining: credits.Credits.MonthlyCredits,
			Cap:       math.Max(allowance, credits.Credits.MonthlyCredits),
			ResetsAt:  periodEnd,
		}
	}
	snap.Session = pctWindowFromAccount(credits.WindowLimits.FiveHour)
	snap.Weekly = pctWindowFromAccount(credits.WindowLimits.Weekly)
	return snap, nil
}

// pctWindowFromAccount converts one CommandCode rate-limit window into the
// predictor's percent meter. A window with no cap and no exceeded flag is an
// inactive (unlimited) gate and maps to nil — the sampler does not invent a
// full window that could never drain.
func pctWindowFromAccount(w accountWindow) *predictor.PctWindow {
	switch {
	case w.Exceeded:
		return &predictor.PctWindow{RemainingPercent: 0}
	case w.Cap <= 0:
		return nil
	}
	pct := (1 - w.Used/w.Cap) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	out := predictor.PctWindow{RemainingPercent: pct}
	if w.ResetAt > 0 {
		out.ResetsAt = time.UnixMilli(w.ResetAt).UTC()
	}
	return &out
}

// parseRFC3339Time converts an upstream ISO instant to UTC; zero when
// unparseable (the predictor treats a zero reset as "no reset bound").
func parseRFC3339Time(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
