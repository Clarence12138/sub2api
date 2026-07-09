package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

func DefaultOpenAIQuotaSubscriptionSyncConfig() OpenAIQuotaSubscriptionSyncConfig {
	return OpenAIQuotaSubscriptionSyncConfig{
		Enabled:             false,
		PollIntervalSeconds: defaultOpenAIQuotaSubscriptionSyncPollIntervalSeconds,
		Rules:               []OpenAIQuotaSubscriptionSyncRule{},
	}
}

func DefaultOpenAIQuotaSubscriptionSyncState() OpenAIQuotaSubscriptionSyncState {
	return OpenAIQuotaSubscriptionSyncState{
		Rules: map[string]OpenAIQuotaSubscriptionSyncRuleState{},
	}
}

func (s *OpenAIQuotaSubscriptionSyncService) GetView(ctx context.Context) (*OpenAIQuotaSubscriptionSyncView, error) {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.getState(ctx)
	if err != nil {
		return nil, err
	}
	return &OpenAIQuotaSubscriptionSyncView{Config: cfg, State: state}, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) GetConfig(ctx context.Context) (OpenAIQuotaSubscriptionSyncConfig, error) {
	cfg := DefaultOpenAIQuotaSubscriptionSyncConfig()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIQuotaSubscriptionSyncConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("get openai quota subscription sync config: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("parse openai quota subscription sync config: %w", err)
	}
	normalizeOpenAIQuotaSubscriptionSyncConfigDefaults(&cfg)
	return cfg, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) UpdateConfig(ctx context.Context, cfg OpenAIQuotaSubscriptionSyncConfig) (*OpenAIQuotaSubscriptionSyncView, error) {
	normalized, err := s.normalizeAndValidateConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal openai quota subscription sync config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIQuotaSubscriptionSyncConfig, string(raw)); err != nil {
		return nil, fmt.Errorf("save openai quota subscription sync config: %w", err)
	}
	return s.GetView(ctx)
}

func (s *OpenAIQuotaSubscriptionSyncService) getState(ctx context.Context) (OpenAIQuotaSubscriptionSyncState, error) {
	state := DefaultOpenAIQuotaSubscriptionSyncState()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIQuotaSubscriptionSyncState)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return state, nil
		}
		return state, fmt.Errorf("get openai quota subscription sync state: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return state, fmt.Errorf("parse openai quota subscription sync state: %w", err)
	}
	if state.Rules == nil {
		state.Rules = map[string]OpenAIQuotaSubscriptionSyncRuleState{}
	}
	return state, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) saveState(ctx context.Context, state OpenAIQuotaSubscriptionSyncState) error {
	if state.Rules == nil {
		state.Rules = map[string]OpenAIQuotaSubscriptionSyncRuleState{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal openai quota subscription sync state: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIQuotaSubscriptionSyncState, string(raw)); err != nil {
		return fmt.Errorf("save openai quota subscription sync state: %w", err)
	}
	return nil
}

func (s *OpenAIQuotaSubscriptionSyncService) normalizeAndValidateConfig(
	ctx context.Context,
	cfg OpenAIQuotaSubscriptionSyncConfig,
) (OpenAIQuotaSubscriptionSyncConfig, error) {
	if cfg.PollIntervalSeconds < 0 {
		return cfg, syncConfigBadRequest("poll_interval_seconds cannot be negative")
	}
	normalizeOpenAIQuotaSubscriptionSyncConfigDefaults(&cfg)
	seenRuleIDs := make(map[string]struct{}, len(cfg.Rules))
	for i := range cfg.Rules {
		rule, err := s.normalizeAndValidateRule(ctx, cfg.Rules[i], i, seenRuleIDs)
		if err != nil {
			return cfg, err
		}
		cfg.Rules[i] = rule
	}
	return cfg, nil
}

func normalizeOpenAIQuotaSubscriptionSyncConfigDefaults(cfg *OpenAIQuotaSubscriptionSyncConfig) {
	if cfg.PollIntervalSeconds <= 0 {
		cfg.PollIntervalSeconds = defaultOpenAIQuotaSubscriptionSyncPollIntervalSeconds
	}
	if cfg.Rules == nil {
		cfg.Rules = []OpenAIQuotaSubscriptionSyncRule{}
	}
	for i := range cfg.Rules {
		if strings.TrimSpace(cfg.Rules[i].SourceWindow) == "" {
			cfg.Rules[i].SourceWindow = OpenAIQuotaSubscriptionSyncWindow5H
		}
	}
}

func (s *OpenAIQuotaSubscriptionSyncService) normalizeAndValidateRule(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	index int,
	seenRuleIDs map[string]struct{},
) (OpenAIQuotaSubscriptionSyncRule, error) {
	rule.ID = strings.TrimSpace(rule.ID)
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		rule.Name = fmt.Sprintf("Rule %d", index+1)
	}
	if _, exists := seenRuleIDs[rule.ID]; exists {
		return rule, syncConfigBadRequest(fmt.Sprintf("rule[%d]: duplicate id %q", index, rule.ID))
	}
	seenRuleIDs[rule.ID] = struct{}{}
	if err := s.validateRuleRefs(ctx, rule, index); err != nil {
		return rule, err
	}
	return rule, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) validateRuleRefs(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	index int,
) error {
	if !validOpenAIQuotaSyncWindow(rule.SourceWindow) {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: source_window must be codex_5h or codex_7d", index))
	}
	if !rule.ResetDaily && !rule.ResetWeekly && !rule.ResetMonthly {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: at least one reset window must be selected", index))
	}
	if len(rule.TargetGroupIDs) == 0 {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: at least one target group must be selected", index))
	}
	if err := s.validateSourceAccount(ctx, rule.SourceAccountID, index); err != nil {
		return err
	}
	return s.validateTargetGroups(ctx, &rule, index)
}

func (s *OpenAIQuotaSubscriptionSyncService) validateSourceAccount(ctx context.Context, accountID int64, index int) error {
	if accountID <= 0 {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: source_account_id is required", index))
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: source account must be OpenAI OAuth", index))
	}
	return nil
}

func (s *OpenAIQuotaSubscriptionSyncService) validateTargetGroups(
	ctx context.Context,
	rule *OpenAIQuotaSubscriptionSyncRule,
	index int,
) error {
	ids := uniquePositiveInt64s(rule.TargetGroupIDs)
	if len(ids) == 0 {
		return syncConfigBadRequest(fmt.Sprintf("rule[%d]: at least one target group must be selected", index))
	}
	for _, groupID := range ids {
		group, err := s.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return err
		}
		if group == nil || group.Platform != PlatformOpenAI || !group.IsSubscriptionType() {
			return syncConfigBadRequest(fmt.Sprintf("rule[%d]: target group %d must be an OpenAI subscription group", index, groupID))
		}
	}
	rule.TargetGroupIDs = ids
	return nil
}

func validOpenAIQuotaSyncWindow(window string) bool {
	switch window {
	case OpenAIQuotaSubscriptionSyncWindow5H, OpenAIQuotaSubscriptionSyncWindow7D:
		return true
	default:
		return false
	}
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func syncConfigBadRequest(message string) error {
	return infraerrors.BadRequest("OPENAI_QUOTA_SUBSCRIPTION_SYNC_INVALID_CONFIG", message)
}
