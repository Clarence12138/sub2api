package service

import (
	"fmt"
	"time"
)

const codexBengalfoxMeteredFeature = "codex_bengalfox"

type openAIQuotaSyncWindowCandidate struct {
	role   string
	window *OpenAIRateLimitWindow
}

func extractOpenAIQuotaSyncWindowObservation(
	usage *OpenAIQuotaUsage,
	sourceWindow string,
	observedAt time.Time,
) (openAIQuotaSubscriptionSyncWindowObservation, error) {
	if usage == nil {
		return openAIQuotaSubscriptionSyncWindowObservation{}, fmt.Errorf("openai quota usage is empty")
	}
	for _, candidate := range openAIQuotaSyncWindowCandidates(usage) {
		if candidate.window == nil || !matchesOpenAIQuotaSyncWindow(candidate, sourceWindow) {
			continue
		}
		resetAt, err := openAIQuotaSyncResetAt(usage, candidate.window, observedAt)
		if err != nil {
			return openAIQuotaSubscriptionSyncWindowObservation{}, err
		}
		return openAIQuotaSubscriptionSyncWindowObservation{
			UsedPercent: candidate.window.UsedPercent,
			ResetAt:     resetAt.UTC(),
		}, nil
	}
	return openAIQuotaSubscriptionSyncWindowObservation{}, fmt.Errorf("source window %s not found in OpenAI quota usage", sourceWindow)
}

func openAIQuotaSyncWindowCandidates(usage *OpenAIQuotaUsage) []openAIQuotaSyncWindowCandidate {
	if usage == nil {
		return nil
	}
	for i := range usage.AdditionalRateLimits {
		additional := usage.AdditionalRateLimits[i]
		if additional.MeteredFeature == codexBengalfoxMeteredFeature && additional.RateLimit != nil {
			return openAIQuotaSyncRateLimitCandidates(additional.RateLimit)
		}
	}
	return openAIQuotaSyncRateLimitCandidates(usage.RateLimit)
}

func openAIQuotaSyncRateLimitCandidates(rateLimit *OpenAIRateLimit) []openAIQuotaSyncWindowCandidate {
	if rateLimit == nil {
		return nil
	}
	return []openAIQuotaSyncWindowCandidate{
		{role: "primary", window: rateLimit.PrimaryWindow},
		{role: "secondary", window: rateLimit.SecondaryWindow},
	}
}

func matchesOpenAIQuotaSyncWindow(candidate openAIQuotaSyncWindowCandidate, sourceWindow string) bool {
	seconds := candidate.window.LimitWindowSeconds
	if seconds > 0 {
		switch sourceWindow {
		case OpenAIQuotaSubscriptionSyncWindow5H:
			return seconds <= int64(6*time.Hour/time.Second)
		case OpenAIQuotaSubscriptionSyncWindow7D:
			return seconds >= int64(24*time.Hour/time.Second)
		default:
			return false
		}
	}
	return matchesLegacyOpenAIQuotaSyncWindowRole(candidate.role, sourceWindow)
}

func matchesLegacyOpenAIQuotaSyncWindowRole(role string, sourceWindow string) bool {
	switch sourceWindow {
	case OpenAIQuotaSubscriptionSyncWindow5H:
		return role == "secondary"
	case OpenAIQuotaSubscriptionSyncWindow7D:
		return role == "primary"
	default:
		return false
	}
}

func openAIQuotaSyncResetAt(
	usage *OpenAIQuotaUsage,
	window *OpenAIRateLimitWindow,
	observedAt time.Time,
) (time.Time, error) {
	if window.ResetAt > 0 {
		return time.Unix(window.ResetAt, 0).UTC(), nil
	}
	if window.ResetAfterSeconds == 0 {
		return time.Time{}, fmt.Errorf("OpenAI quota window reset_at is missing")
	}
	base := observedAt.UTC()
	if usage != nil && usage.FetchedAt > 0 {
		base = time.Unix(usage.FetchedAt, 0).UTC()
	}
	return base.Add(time.Duration(window.ResetAfterSeconds) * time.Second).UTC(), nil
}
