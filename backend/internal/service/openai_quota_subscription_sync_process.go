package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *OpenAIQuotaSubscriptionSyncService) ProcessOnce(ctx context.Context) error {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	state, err := s.getState(ctx)
	if err != nil {
		return err
	}
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
			continue
		}
		s.processRule(ctx, rule, &state)
		if err := s.saveState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func (s *OpenAIQuotaSubscriptionSyncService) processRule(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	state *OpenAIQuotaSubscriptionSyncState,
) {
	now := time.Now().UTC()
	ruleState := getOpenAIQuotaSyncRuleState(state, rule.ID)
	if err := s.validateRuleRefs(ctx, rule, 0); err != nil {
		recordOpenAIQuotaSyncRuleError(&ruleState, now, err)
		state.Rules[rule.ID] = ruleState
		return
	}
	usage, err := s.quotaService.QueryUsage(ctx, rule.SourceAccountID)
	polledAt := time.Now().UTC()
	if err != nil {
		recordOpenAIQuotaSyncRuleError(&ruleState, polledAt, err)
		ruleState.LastPolledAt = &polledAt
		state.Rules[rule.ID] = ruleState
		return
	}
	s.applyObservation(ctx, rule, usage, polledAt, &ruleState)
	state.Rules[rule.ID] = ruleState
}

func (s *OpenAIQuotaSubscriptionSyncService) applyObservation(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	usage *OpenAIQuotaUsage,
	polledAt time.Time,
	ruleState *OpenAIQuotaSubscriptionSyncRuleState,
) {
	obs, err := extractOpenAIQuotaSyncWindowObservation(usage, rule.SourceWindow, polledAt)
	ruleState.LastPolledAt = &polledAt
	if err != nil {
		recordOpenAIQuotaSyncRuleError(ruleState, polledAt, err)
		return
	}
	ruleState.LastUsedPercent = &obs.UsedPercent
	if ruleState.LastSeenResetAt == nil {
		setOpenAIQuotaSyncBaseline(ruleState, obs.ResetAt)
		return
	}
	if obs.ResetAt.Before(ruleState.LastSeenResetAt.UTC()) {
		err := fmt.Errorf("observed reset_at moved backward: last_seen=%s observed=%s",
			ruleState.LastSeenResetAt.UTC().Format(time.RFC3339),
			obs.ResetAt.UTC().Format(time.RFC3339))
		recordOpenAIQuotaSyncRuleError(ruleState, polledAt, err)
		return
	}
	if shouldTriggerOpenAIQuotaSubscriptionSync(*ruleState.LastSeenResetAt, obs.ResetAt, polledAt) {
		s.triggerOpenAIQuotaSubscriptionSync(ctx, rule, obs.ResetAt, polledAt, ruleState)
		return
	}
	setOpenAIQuotaSyncSuccess(ruleState, obs.ResetAt)
}

func (s *OpenAIQuotaSubscriptionSyncService) triggerOpenAIQuotaSubscriptionSync(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	newResetAt time.Time,
	polledAt time.Time,
	ruleState *OpenAIQuotaSubscriptionSyncRuleState,
) {
	officialResetAt := ruleState.LastSeenResetAt.UTC()
	count, err := s.executeResetEvent(ctx, rule, officialResetAt)
	if err != nil {
		recordOpenAIQuotaSyncRuleError(ruleState, polledAt, err)
		return
	}
	triggeredAt := time.Now().UTC()
	ruleState.LastTriggeredAt = &triggeredAt
	ruleState.LastResetCount = count
	setOpenAIQuotaSyncSuccess(ruleState, newResetAt)
}

func shouldTriggerOpenAIQuotaSubscriptionSync(lastSeenResetAt, observedResetAt, now time.Time) bool {
	return !lastSeenResetAt.UTC().After(now.UTC()) && observedResetAt.UTC().After(lastSeenResetAt.UTC())
}

func setOpenAIQuotaSyncBaseline(ruleState *OpenAIQuotaSubscriptionSyncRuleState, resetAt time.Time) {
	setOpenAIQuotaSyncSuccess(ruleState, resetAt)
	ruleState.LastResetCount = 0
	ruleState.LastTriggeredAt = nil
}

func setOpenAIQuotaSyncSuccess(ruleState *OpenAIQuotaSubscriptionSyncRuleState, resetAt time.Time) {
	resetAt = resetAt.UTC()
	ruleState.LastSeenResetAt = &resetAt
	ruleState.LastError = ""
	ruleState.LastErrorAt = nil
}

func recordOpenAIQuotaSyncRuleError(ruleState *OpenAIQuotaSubscriptionSyncRuleState, at time.Time, err error) {
	at = at.UTC()
	ruleState.LastError = err.Error()
	ruleState.LastErrorAt = &at
}

func getOpenAIQuotaSyncRuleState(
	state *OpenAIQuotaSubscriptionSyncState,
	ruleID string,
) OpenAIQuotaSubscriptionSyncRuleState {
	if state.Rules == nil {
		state.Rules = map[string]OpenAIQuotaSubscriptionSyncRuleState{}
	}
	ruleState := state.Rules[ruleID]
	if ruleState.RuleID == "" {
		ruleState.RuleID = ruleID
	}
	return ruleState
}

func (s *OpenAIQuotaSubscriptionSyncService) executeResetEvent(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	officialResetAt time.Time,
) (int, error) {
	record, started, err := s.beginResetEvent(ctx, rule, officialResetAt)
	if err != nil || !started {
		return 0, err
	}
	count, err := s.resetTargetGroupSubscriptions(ctx, rule, officialResetAt)
	if err != nil {
		if markErr := s.markResetEventFailed(ctx, record.ID, err); markErr != nil {
			return count, fmt.Errorf("%w; additionally failed to mark idempotency retryable: %v", err, markErr)
		}
		return count, err
	}
	if err := s.markResetEventSucceeded(ctx, record.ID, count); err != nil {
		return count, err
	}
	return count, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) beginResetEvent(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	officialResetAt time.Time,
) (*IdempotencyRecord, bool, error) {
	if s.idempotencyRepo == nil {
		return nil, false, infraerrors.ServiceUnavailable("IDEMPOTENCY_STORE_UNAVAILABLE", "idempotency repository is not configured")
	}
	now := time.Now().UTC()
	key := openAIQuotaSubscriptionSyncIdempotencyKey(rule, officialResetAt)
	fingerprint, err := BuildIdempotencyFingerprint("POST", "/internal/openai-quota-subscription-sync", "system", map[string]any{
		"rule_id":           rule.ID,
		"source_window":     rule.SourceWindow,
		"official_reset_at": officialResetAt.UTC().Format(time.RFC3339),
		"target_group_ids":  rule.TargetGroupIDs,
		"reset_daily":       rule.ResetDaily,
		"reset_weekly":      rule.ResetWeekly,
		"reset_monthly":     rule.ResetMonthly,
	})
	if err != nil {
		return nil, false, err
	}
	record := &IdempotencyRecord{
		Scope:              openAIQuotaSubscriptionSyncIDScope,
		IdempotencyKeyHash: HashIdempotencyKey(key),
		RequestFingerprint: fingerprint,
		Status:             IdempotencyStatusProcessing,
		LockedUntil:        timePtr(now.Add(30 * time.Minute)),
		ExpiresAt:          now.Add(30 * 24 * time.Hour),
	}
	created, err := s.idempotencyRepo.CreateProcessing(ctx, record)
	if err != nil {
		return nil, false, err
	}
	if created {
		return record, true, nil
	}
	return s.reclaimExistingResetEvent(ctx, record, now)
}

func (s *OpenAIQuotaSubscriptionSyncService) reclaimExistingResetEvent(
	ctx context.Context,
	record *IdempotencyRecord,
	now time.Time,
) (*IdempotencyRecord, bool, error) {
	existing, err := s.idempotencyRepo.GetByScopeAndKeyHash(ctx, record.Scope, record.IdempotencyKeyHash)
	if err != nil || existing == nil {
		return nil, false, err
	}
	if existing.RequestFingerprint != record.RequestFingerprint {
		return nil, false, ErrIdempotencyKeyConflict
	}
	if existing.Status == IdempotencyStatusSucceeded {
		return existing, false, nil
	}
	if existing.LockedUntil != nil && existing.LockedUntil.After(now) {
		if existing.Status == IdempotencyStatusFailedRetryable {
			return nil, false, ErrIdempotencyRetryBackoff
		}
		return nil, false, ErrIdempotencyInProgress
	}
	ok, err := s.idempotencyRepo.TryReclaim(
		ctx,
		existing.ID,
		existing.Status,
		now,
		now.Add(30*time.Minute),
		now.Add(30*24*time.Hour),
	)
	if err != nil || !ok {
		return nil, false, err
	}
	existing.Status = IdempotencyStatusProcessing
	existing.LockedUntil = timePtr(now.Add(30 * time.Minute))
	existing.ExpiresAt = now.Add(30 * 24 * time.Hour)
	return existing, true, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) markResetEventFailed(ctx context.Context, id int64, err error) error {
	if s.idempotencyRepo == nil || id <= 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.idempotencyRepo.MarkFailedRetryable(ctx, id, err.Error(), now.Add(5*time.Minute), now.Add(30*24*time.Hour))
}

func (s *OpenAIQuotaSubscriptionSyncService) markResetEventSucceeded(ctx context.Context, id int64, count int) error {
	raw, err := json.Marshal(map[string]any{"reset_count": count})
	if err != nil {
		return err
	}
	return s.idempotencyRepo.MarkSucceeded(ctx, id, 200, string(raw), time.Now().UTC().Add(30*24*time.Hour))
}

func openAIQuotaSubscriptionSyncIdempotencyKey(rule OpenAIQuotaSubscriptionSyncRule, officialResetAt time.Time) string {
	return rule.ID + ":" + rule.SourceWindow + ":" + officialResetAt.UTC().Format(time.RFC3339)
}

func timePtr(value time.Time) *time.Time {
	return &value
}
