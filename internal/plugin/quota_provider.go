package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type quotaProvider struct {
	manager        *Manager
	hostCallbackID string
}

var _ pluginapi.QuotaProvider = quotaProvider{}

func newQuotaProvider(manager *Manager, hostCallbackID string) quotaProvider {
	return quotaProvider{manager: manager, hostCallbackID: hostCallbackID}
}

func (quotaProvider) Identifier() string { return ProviderID }

func (quotaProvider) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{ProviderID},
		DisplayName:        "CommandCode",
		SupportsReset:      false,
	}, nil
}

func (p quotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	if strings.TrimSpace(req.Provider) != ProviderID {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("quota credential is not owned by commandcode")
	}
	if p.manager == nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("quota provider unavailable")
	}
	bridge := p.manager.bridge.withHostCallbackID(p.hostCallbackID)
	storageJSON := req.StorageJSON
	if len(storageJSON) == 0 {
		if strings.TrimSpace(req.AuthIndex) == "" || bridge == nil {
			return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota credential storage is unavailable")
		}
		var err error
		storageJSON, err = bridge.AuthGet(ctx, req.AuthIndex)
		if err != nil {
			return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota credential storage is unavailable")
		}
	}
	key, err := quotaCredential(req.Attributes, storageJSON)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}
	p.manager.mu.RLock()
	baseURL, timeout := p.manager.cfg.BaseURL, p.manager.cfg.RequestTimeout
	p.manager.mu.RUnlock()
	usage, _, _, err := fetchQuota(ctx, bridge, baseURL, timeout, key)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}
	return nativeQuotaResponse(usage), nil
}

func (quotaProvider) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{}, fmt.Errorf("commandcode quota reset is unsupported")
}

func quotaCredential(attributes map[string]string, storageJSON []byte) (string, error) {
	var stored struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}
	if len(storageJSON) == 0 || json.Unmarshal(storageJSON, &stored) != nil {
		return "", fmt.Errorf("commandcode quota credential storage is invalid")
	}
	typeID := strings.TrimSpace(stored.Type)
	providerID := strings.TrimSpace(stored.Provider)
	if (typeID != "" && typeID != ProviderID) || (providerID != "" && providerID != ProviderID) ||
		(typeID == "" && providerID == "") {
		return "", fmt.Errorf("quota credential is not owned by commandcode")
	}
	key := strings.TrimSpace(stored.APIKey)
	if key == "" {
		return "", fmt.Errorf("commandcode quota credential has no api key")
	}
	if attributeKey := strings.TrimSpace(attributes["api_key"]); attributeKey != "" && attributeKey != key {
		return "", fmt.Errorf("commandcode quota credential is inconsistent")
	}
	return key, nil
}

func nativeQuotaResponse(usage quotaUsage) pluginapi.QuotaFetchResponse {
	resp := pluginapi.QuotaFetchResponse{
		Summary: []pluginapi.QuotaMetric{
			{Key: "credits_left", Label: "Credits left", Value: usage.CreditsLeft, Unit: "credits", Format: "number"},
			{Key: "purchased_credits", Label: "Purchased credits", Value: usage.PurchasedCredits, Unit: "credits", Format: "number"},
		},
	}
	if usage.Plan != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{Plan: usage.Plan, TierName: usage.Plan}
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 3)
	if usage.Month != nil && usage.Month.Cap > 0 {
		buckets = append(buckets, nativeQuotaBucket("month", "Monthly credits", *usage.Month))
	}
	if bucket, ok := nativeRateBucket("five_hour", "Five-hour window", usage.FiveHour); ok {
		buckets = append(buckets, bucket)
	}
	if bucket, ok := nativeRateBucket("weekly", "Weekly window", usage.Weekly); ok {
		buckets = append(buckets, bucket)
	}
	if len(buckets) > 0 {
		resp.Groups = []pluginapi.QuotaGroup{{DisplayName: "CommandCode quota", Buckets: buckets}}
	}
	return resp
}

func nativeRateBucket(window, description string, value quotaWindow) (pluginapi.QuotaBucket, bool) {
	if value.Status == "unlimited" {
		return pluginapi.QuotaBucket{Window: window, RemainingFraction: 1, ResetTime: value.ResetsAt, Description: description + " (unlimited)"}, true
	}
	if value.Cap <= 0 {
		return pluginapi.QuotaBucket{}, false
	}
	return nativeQuotaBucket(window, description, value), true
}

func nativeQuotaBucket(window, description string, value quotaWindow) pluginapi.QuotaBucket {
	remaining := 1 - value.Used/value.Cap
	remaining = math.Max(0, math.Min(1, remaining))
	return pluginapi.QuotaBucket{Window: window, RemainingFraction: remaining, ResetTime: value.ResetsAt, Description: description}
}
