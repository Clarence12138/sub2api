package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type openAIQuotaSyncSettingRepoStub struct {
	values map[string]string
}

func newOpenAIQuotaSyncSettingRepoStub() *openAIQuotaSyncSettingRepoStub {
	return &openAIQuotaSyncSettingRepoStub{values: map[string]string{}}
}

func (r *openAIQuotaSyncSettingRepoStub) Get(_ context.Context, key string) (*Setting, error) {
	value, err := r.GetValue(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *openAIQuotaSyncSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *openAIQuotaSyncSettingRepoStub) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *openAIQuotaSyncSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *openAIQuotaSyncSettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *openAIQuotaSyncSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *openAIQuotaSyncSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type openAIQuotaSyncAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *openAIQuotaSyncAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	cp := *account
	return &cp, nil
}

type openAIQuotaSyncGroupRepoStub struct {
	groupRepoNoop
	groups map[int64]*Group
}

func (r *openAIQuotaSyncGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group := r.groups[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	cp := *group
	return &cp, nil
}

type openAIQuotaSyncUserSubRepoStub struct {
	userSubRepoNoop
	byGroup map[int64][]UserSubscription
	byID    map[int64]*UserSubscription
	resets  []openAIQuotaSyncResetCall
	err     error
}

type openAIQuotaSyncResetCall struct {
	subID       int64
	windowStart time.Time
}

func newOpenAIQuotaSyncUserSubRepoStub(subs ...UserSubscription) *openAIQuotaSyncUserSubRepoStub {
	repo := &openAIQuotaSyncUserSubRepoStub{
		byGroup: map[int64][]UserSubscription{},
		byID:    map[int64]*UserSubscription{},
	}
	for i := range subs {
		sub := subs[i]
		cp := sub
		repo.byGroup[sub.GroupID] = append(repo.byGroup[sub.GroupID], sub)
		repo.byID[sub.ID] = &cp
	}
	return repo
}

func (r *openAIQuotaSyncUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	sub := r.byID[id]
	if sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *sub
	return &cp, nil
}

func (r *openAIQuotaSyncUserSubRepoStub) ListByGroupID(
	_ context.Context,
	groupID int64,
	params pagination.PaginationParams,
) ([]UserSubscription, *pagination.PaginationResult, error) {
	if params.Page > 1 {
		return nil, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
	}
	subs := append([]UserSubscription(nil), r.byGroup[groupID]...)
	return subs, &pagination.PaginationResult{
		Total:    int64(len(subs)),
		Page:     1,
		PageSize: params.PageSize,
		Pages:    1,
	}, nil
}

func (r *openAIQuotaSyncUserSubRepoStub) ResetDailyUsage(_ context.Context, id int64, windowStart time.Time) error {
	if r.err != nil {
		return r.err
	}
	sub := r.byID[id]
	if sub == nil {
		return ErrSubscriptionNotFound
	}
	sub.DailyUsageUSD = 0
	sub.DailyWindowStart = &windowStart
	r.resets = append(r.resets, openAIQuotaSyncResetCall{subID: id, windowStart: windowStart})
	return nil
}

type openAIQuotaSyncQuotaQuerierStub struct {
	usage *OpenAIQuotaUsage
	err   error
	calls int
}

func (q *openAIQuotaSyncQuotaQuerierStub) QueryUsage(context.Context, int64) (*OpenAIQuotaUsage, error) {
	q.calls++
	return q.usage, q.err
}

func newOpenAIQuotaSyncTestService(
	settingRepo *openAIQuotaSyncSettingRepoStub,
	userSubRepo *openAIQuotaSyncUserSubRepoStub,
	quota *openAIQuotaSyncQuotaQuerierStub,
) *OpenAIQuotaSubscriptionSyncService {
	accountRepo := &openAIQuotaSyncAccountRepoStub{accounts: map[int64]*Account{
		10: {ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	groupRepo := &openAIQuotaSyncGroupRepoStub{groups: map[int64]*Group{
		20: {ID: 20, Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
	}}
	subSvc := NewSubscriptionService(groupRepo, userSubRepo, nil, nil, nil)
	return NewOpenAIQuotaSubscriptionSyncService(
		settingRepo,
		accountRepo,
		groupRepo,
		userSubRepo,
		subSvc,
		quota,
		newInMemoryIdempotencyRepo(),
	)
}

func openAIQuotaSyncTestConfig() OpenAIQuotaSubscriptionSyncConfig {
	return OpenAIQuotaSubscriptionSyncConfig{
		Enabled:             true,
		PollIntervalSeconds: 300,
		Rules: []OpenAIQuotaSubscriptionSyncRule{{
			ID:              "rule-1",
			Name:            "Codex 5h",
			Enabled:         true,
			SourceAccountID: 10,
			SourceWindow:    OpenAIQuotaSubscriptionSyncWindow5H,
			TargetGroupIDs:  []int64{20},
			ResetDaily:      true,
		}},
	}
}

func openAIQuotaSyncUsage(resetAt time.Time, usedPercent float64) *OpenAIQuotaUsage {
	return &OpenAIQuotaUsage{
		FetchedAt: time.Now().UTC().Unix(),
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
			MeteredFeature: codexBengalfoxMeteredFeature,
			RateLimit: &OpenAIRateLimit{
				PrimaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        1,
					LimitWindowSeconds: int64(7 * 24 * time.Hour / time.Second),
					ResetAt:            resetAt.Add(7 * 24 * time.Hour).Unix(),
				},
				SecondaryWindow: &OpenAIRateLimitWindow{
					UsedPercent:        usedPercent,
					LimitWindowSeconds: int64(5 * time.Hour / time.Second),
					ResetAt:            resetAt.Unix(),
				},
			},
		}},
	}
}

func TestOpenAIQuotaSubscriptionSync_UpdateConfigValidation(t *testing.T) {
	settingRepo := newOpenAIQuotaSyncSettingRepoStub()
	userSubRepo := newOpenAIQuotaSyncUserSubRepoStub()
	svc := newOpenAIQuotaSyncTestService(settingRepo, userSubRepo, &openAIQuotaSyncQuotaQuerierStub{})

	cfg := openAIQuotaSyncTestConfig()
	cfg.Rules[0].TargetGroupIDs = nil
	_, err := svc.UpdateConfig(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one target group")

	cfg = openAIQuotaSyncTestConfig()
	cfg.Rules[0].ResetDaily = false
	_, err = svc.UpdateConfig(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one reset window")
}

func TestOpenAIQuotaSubscriptionSync_FirstObservationOnlySavesBaseline(t *testing.T) {
	resetAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	settingRepo := newOpenAIQuotaSyncSettingRepoStub()
	userSubRepo := newOpenAIQuotaSyncUserSubRepoStub(
		UserSubscription{ID: 1, UserID: 1, GroupID: 20, Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(24 * time.Hour)},
	)
	quota := &openAIQuotaSyncQuotaQuerierStub{usage: openAIQuotaSyncUsage(resetAt, 42)}
	svc := newOpenAIQuotaSyncTestService(settingRepo, userSubRepo, quota)
	_, err := svc.UpdateConfig(context.Background(), openAIQuotaSyncTestConfig())
	require.NoError(t, err)

	err = svc.ProcessOnce(context.Background())

	require.NoError(t, err)
	require.Empty(t, userSubRepo.resets)
	state, err := svc.getState(context.Background())
	require.NoError(t, err)
	require.WithinDuration(t, resetAt, *state.Rules["rule-1"].LastSeenResetAt, time.Second)
	require.Equal(t, 42.0, *state.Rules["rule-1"].LastUsedPercent)
}

func TestOpenAIQuotaSubscriptionSync_ResetOnNewWindowAndSkipDuplicate(t *testing.T) {
	now := time.Now().UTC()
	oldResetAt := now.Add(-time.Minute).Truncate(time.Second)
	newResetAt := now.Add(5 * time.Hour).Truncate(time.Second)
	settingRepo := newOpenAIQuotaSyncSettingRepoStub()
	userSubRepo := newOpenAIQuotaSyncUserSubRepoStub(
		UserSubscription{ID: 1, UserID: 1, GroupID: 20, Status: SubscriptionStatusActive, ExpiresAt: now.Add(24 * time.Hour), DailyUsageUSD: 9},
		UserSubscription{ID: 2, UserID: 2, GroupID: 20, Status: SubscriptionStatusSuspended, ExpiresAt: now.Add(24 * time.Hour), DailyUsageUSD: 9},
		UserSubscription{ID: 3, UserID: 3, GroupID: 20, Status: SubscriptionStatusActive, ExpiresAt: now.Add(-time.Hour), DailyUsageUSD: 9},
	)
	quota := &openAIQuotaSyncQuotaQuerierStub{usage: openAIQuotaSyncUsage(newResetAt, 2)}
	svc := newOpenAIQuotaSyncTestService(settingRepo, userSubRepo, quota)
	_, err := svc.UpdateConfig(context.Background(), openAIQuotaSyncTestConfig())
	require.NoError(t, err)
	require.NoError(t, svc.saveState(context.Background(), OpenAIQuotaSubscriptionSyncState{
		Rules: map[string]OpenAIQuotaSubscriptionSyncRuleState{
			"rule-1": {RuleID: "rule-1", LastSeenResetAt: &oldResetAt},
		},
	}))

	err = svc.ProcessOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, userSubRepo.resets, 1)
	require.Equal(t, int64(1), userSubRepo.resets[0].subID)
	require.WithinDuration(t, oldResetAt, userSubRepo.resets[0].windowStart, time.Second)

	err = svc.ProcessOnce(context.Background())
	require.NoError(t, err)
	require.Len(t, userSubRepo.resets, 1)
	state, err := svc.getState(context.Background())
	require.NoError(t, err)
	require.WithinDuration(t, newResetAt, *state.Rules["rule-1"].LastSeenResetAt, time.Second)
	require.Equal(t, 1, state.Rules["rule-1"].LastResetCount)
}

func TestOpenAIQuotaSubscriptionSync_QueryFailureRecordsError(t *testing.T) {
	upstreamErr := errors.New("upstream unavailable")
	oldResetAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	settingRepo := newOpenAIQuotaSyncSettingRepoStub()
	userSubRepo := newOpenAIQuotaSyncUserSubRepoStub()
	quota := &openAIQuotaSyncQuotaQuerierStub{err: upstreamErr}
	svc := newOpenAIQuotaSyncTestService(settingRepo, userSubRepo, quota)
	_, err := svc.UpdateConfig(context.Background(), openAIQuotaSyncTestConfig())
	require.NoError(t, err)
	require.NoError(t, svc.saveState(context.Background(), OpenAIQuotaSubscriptionSyncState{
		Rules: map[string]OpenAIQuotaSubscriptionSyncRuleState{
			"rule-1": {RuleID: "rule-1", LastSeenResetAt: &oldResetAt},
		},
	}))

	err = svc.ProcessOnce(context.Background())

	require.NoError(t, err)
	state, err := svc.getState(context.Background())
	require.NoError(t, err)
	require.Contains(t, state.Rules["rule-1"].LastError, upstreamErr.Error())
	require.WithinDuration(t, oldResetAt, *state.Rules["rule-1"].LastSeenResetAt, time.Second)
}
