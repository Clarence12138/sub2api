package service

import (
	"context"
	"time"
)

const (
	OpenAIQuotaSubscriptionSyncWindow5H = "codex_5h"
	OpenAIQuotaSubscriptionSyncWindow7D = "codex_7d"

	defaultOpenAIQuotaSubscriptionSyncPollIntervalSeconds = 300

	openAIQuotaSubscriptionSyncLeaderLockKey = "openai_quota_subscription_sync:poll"
	openAIQuotaSubscriptionSyncIDScope       = "openai_quota_subscription_sync"
)

// OpenAIQuotaSubscriptionSyncConfig is persisted in settings as JSON.
type OpenAIQuotaSubscriptionSyncConfig struct {
	Enabled             bool                              `json:"enabled"`
	PollIntervalSeconds int                               `json:"poll_interval_seconds"`
	Rules               []OpenAIQuotaSubscriptionSyncRule `json:"rules"`
}

type OpenAIQuotaSubscriptionSyncRule struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Enabled         bool    `json:"enabled"`
	SourceAccountID int64   `json:"source_account_id"`
	SourceWindow    string  `json:"source_window"`
	TargetGroupIDs  []int64 `json:"target_group_ids"`
	ResetDaily      bool    `json:"reset_daily"`
	ResetWeekly     bool    `json:"reset_weekly"`
	ResetMonthly    bool    `json:"reset_monthly"`
}

type OpenAIQuotaSubscriptionSyncState struct {
	Rules map[string]OpenAIQuotaSubscriptionSyncRuleState `json:"rules"`
}

type OpenAIQuotaSubscriptionSyncRuleState struct {
	RuleID          string     `json:"rule_id"`
	LastSeenResetAt *time.Time `json:"last_seen_reset_at,omitempty"`
	LastPolledAt    *time.Time `json:"last_polled_at,omitempty"`
	LastTriggeredAt *time.Time `json:"last_triggered_at,omitempty"`
	LastResetCount  int        `json:"last_reset_count"`
	LastError       string     `json:"last_error,omitempty"`
	LastErrorAt     *time.Time `json:"last_error_at,omitempty"`
	LastUsedPercent *float64   `json:"last_used_percent,omitempty"`
}

type OpenAIQuotaSubscriptionSyncView struct {
	Config OpenAIQuotaSubscriptionSyncConfig `json:"config"`
	State  OpenAIQuotaSubscriptionSyncState  `json:"state"`
}

type openAIQuotaSubscriptionSyncWindowObservation struct {
	UsedPercent float64
	ResetAt     time.Time
}

type OpenAIQuotaUsageQuerier interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
}
