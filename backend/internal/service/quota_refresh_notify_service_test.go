//go:build unit

package service

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type quotaRefreshAccountRepoStub struct {
	mu          sync.Mutex
	updates     []quotaRefreshUpdateCall
	findResults []Account
	findErr     error
	findCalls   int
}

type quotaRefreshUpdateCall struct {
	accountID int64
	updates   map[string]any
}

func (r *quotaRefreshAccountRepoStub) FindByExtraField(_ context.Context, _ string, _ any) ([]Account, error) {
	r.mu.Lock()
	r.findCalls++
	r.mu.Unlock()
	return r.findResults, r.findErr
}

type quotaRefreshLeaderLockStub struct {
	acquire bool
}

func (s *quotaRefreshLeaderLockStub) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	return s.acquire, nil
}

func (s *quotaRefreshLeaderLockStub) ReleaseLeaderLock(context.Context, string, string) error {
	return nil
}

func (r *quotaRefreshAccountRepoStub) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyOfUpdates := make(map[string]any, len(updates))
	for key, value := range updates {
		copyOfUpdates[key] = value
	}
	r.updates = append(r.updates, quotaRefreshUpdateCall{accountID: id, updates: copyOfUpdates})
	return nil
}

func (r *quotaRefreshAccountRepoStub) lastUpdate(t *testing.T) quotaRefreshUpdateCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.updates)
	return r.updates[len(r.updates)-1]
}

type quotaRefreshUsageReaderStub struct {
	mu      sync.Mutex
	usage   *UsageInfo
	err     error
	callIDs []int64
}

func (r *quotaRefreshUsageReaderStub) GetUsage(_ context.Context, accountID int64, _ ...bool) (*UsageInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callIDs = append(r.callIDs, accountID)
	return r.usage, r.err
}

func (r *quotaRefreshUsageReaderStub) calls() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.callIDs...)
}

type quotaRefreshUserRepoStub struct {
	pages       map[int][]User
	pageCount   int
	requested   []pagination.PaginationParams
	filtersSeen []UserListFilters
}

type quotaRefreshEmailSenderStub struct {
	mu       sync.Mutex
	errors   map[string]error
	received []quotaRefreshEmailCall
}

type quotaRefreshEmailCall struct {
	to      string
	subject string
	body    string
}

func (s *quotaRefreshEmailSenderStub) SendEmail(_ context.Context, to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, quotaRefreshEmailCall{to: to, subject: subject, body: body})
	return s.errors[to]
}

func (s *quotaRefreshEmailSenderStub) callsTo(to string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.received {
		if call.to == to {
			count++
		}
	}
	return count
}

type quotaRefreshNotificationSenderStub struct {
	input NotificationEmailSendInput
	err   error
}

func (s *quotaRefreshNotificationSenderStub) Send(_ context.Context, input NotificationEmailSendInput) error {
	s.input = input
	return s.err
}

func (r *quotaRefreshUserRepoStub) ListWithFilters(
	_ context.Context,
	params pagination.PaginationParams,
	filters UserListFilters,
) ([]User, *pagination.PaginationResult, error) {
	r.requested = append(r.requested, params)
	r.filtersSeen = append(r.filtersSeen, filters)
	return r.pages[params.Page], &pagination.PaginationResult{
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    r.pageCount,
	}, nil
}

func quotaRefreshTestAccount(id int64, status string, windows []string, oldWindows map[string]usageWindowSnap) *Account {
	extra := map[string]any{
		extraKeyQuotaRefreshNotifyEnabled: true,
	}
	if windows != nil {
		extra[extraKeyQuotaRefreshNotifyWindows] = windows
	}
	if oldWindows != nil {
		extra[extraKeyQuotaRefreshNotifySnapshot] = snapshotToMap(quotaRefreshSnapshot{
			SampledAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			Windows:   oldWindows,
		})
	}
	return &Account{ID: id, Name: "watched", Platform: PlatformAnthropic, Status: status, Extra: extra}
}

func TestQuotaRefreshProcessAccount_SkipsDisabledAndInactiveAccounts(t *testing.T) {
	usageReader := &quotaRefreshUsageReaderStub{usage: &UsageInfo{FiveHour: &UsageProgress{Utilization: 10}}}
	repo := &quotaRefreshAccountRepoStub{}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, nil, time.Minute)

	disabled := quotaRefreshTestAccount(1, StatusActive, nil, nil)
	disabled.Extra[extraKeyQuotaRefreshNotifyEnabled] = false
	inactive := quotaRefreshTestAccount(2, StatusDisabled, nil, nil)
	svc.processAccount(context.Background(), disabled, []string{"admin@example.com"}, "test")
	svc.processAccount(context.Background(), inactive, []string{"admin@example.com"}, "test")

	assert.Empty(t, usageReader.calls())
	assert.Empty(t, repo.updates)
}

func TestQuotaRefreshRunOnce_NonLeaderDoesNotScanAccounts(t *testing.T) {
	repo := &quotaRefreshAccountRepoStub{}
	svc := NewQuotaRefreshNotifyService(repo, &quotaRefreshUsageReaderStub{}, nil, nil, nil, time.Minute)
	svc.SetLeaderLock(&quotaRefreshLeaderLockStub{acquire: false}, nil)

	svc.runOnce()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Zero(t, repo.findCalls)
}

func TestQuotaRefreshCursor_ContinuesAfterLastProcessedAccount(t *testing.T) {
	svc := NewQuotaRefreshNotifyService(nil, nil, nil, nil, nil, time.Minute)
	accounts := []Account{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}}
	rotated, start := svc.rotateAccounts(accounts)
	require.Equal(t, int64(1), rotated[0].ID)

	svc.advanceCursor(start, 3, len(accounts))
	rotated, _ = svc.rotateAccounts(accounts)

	require.Equal(t, int64(4), rotated[0].ID)
}

func TestQuotaRefreshProcessAccount_ColdStartPersistsSnapshotWithoutNotification(t *testing.T) {
	reset := time.Now().Add(5 * time.Hour)
	usageReader := &quotaRefreshUsageReaderStub{usage: &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 8, ResetsAt: &reset},
	}}
	repo := &quotaRefreshAccountRepoStub{}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, nil, time.Minute)
	account := quotaRefreshTestAccount(42, StatusActive, []string{QuotaRefreshWindowFiveHour}, nil)

	svc.processAccount(context.Background(), account, []string{"admin@example.com"}, "test")

	assert.Equal(t, []int64{42}, usageReader.calls())
	update := repo.lastUpdate(t)
	assert.Equal(t, int64(42), update.accountID)
	assert.Contains(t, update.updates, extraKeyQuotaRefreshNotifySnapshot)
	assert.NotContains(t, update.updates, extraKeyQuotaRefreshLastNotifiedAt)
}

func TestQuotaRefreshProcessAccount_UnselectedRefreshDoesNotNotify(t *testing.T) {
	now := time.Now()
	old5hReset := now.Add(-time.Hour)
	new5hReset := now.Add(5 * time.Hour)
	sevenDayReset := now.Add(4 * 24 * time.Hour)
	account := quotaRefreshTestAccount(7, StatusActive, []string{QuotaRefreshWindowSevenDay}, map[string]usageWindowSnap{
		"five_hour": {Utilization: 95, ResetsAt: &old5hReset},
		"seven_day": {Utilization: 40, ResetsAt: &sevenDayReset},
	})
	usageReader := &quotaRefreshUsageReaderStub{usage: &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 2, ResetsAt: &new5hReset},
		SevenDay: &UsageProgress{Utilization: 41, ResetsAt: &sevenDayReset},
	}}
	repo := &quotaRefreshAccountRepoStub{}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, nil, time.Minute)

	svc.processAccount(context.Background(), account, []string{"admin@example.com"}, "test")

	update := repo.lastUpdate(t)
	assert.Contains(t, update.updates, extraKeyQuotaRefreshNotifySnapshot)
	assert.NotContains(t, update.updates, extraKeyQuotaRefreshLastNotifiedAt)
}

func TestDetectQuotaRefresh_SameCycleReturnsBothSelectedWindows(t *testing.T) {
	now := time.Now()
	old5hReset := now.Add(-time.Hour)
	new5hReset := now.Add(5 * time.Hour)
	old7dReset := now.Add(-2 * time.Hour)
	new7dReset := now.Add(7 * 24 * time.Hour)
	oldWindows := map[string]usageWindowSnap{
		"five_hour": {Utilization: 92, ResetsAt: &old5hReset},
		"seven_day": {Utilization: 87, ResetsAt: &old7dReset},
	}
	newWindows := map[string]usageWindowSnap{
		"five_hour": {Utilization: 3, ResetsAt: &new5hReset},
		"seven_day": {Utilization: 4, ResetsAt: &new7dReset},
	}

	refreshed := detectQuotaRefresh(oldWindows, newWindows, now)
	refreshed = filterRefreshedBySelectedWindows(refreshed, []string{
		QuotaRefreshWindowFiveHour,
		QuotaRefreshWindowSevenDay,
	})

	require.Len(t, refreshed, 2)
	assert.ElementsMatch(t, []string{"five_hour", "seven_day"}, []string{refreshed[0].Key, refreshed[1].Key})
}

func TestFilterDebouncedWindows_AppliesDebouncePerWindow(t *testing.T) {
	now := time.Now().UTC()
	refreshed := []refreshedWindow{{Key: "five_hour"}, {Key: "seven_day"}}
	lastNotified := map[string]time.Time{
		"five_hour": now.Add(-time.Minute),
		"seven_day": now.Add(-time.Hour),
	}

	got := filterDebouncedWindows(refreshed, lastNotified, now)

	require.Len(t, got, 1)
	assert.Equal(t, "seven_day", got[0].Key)
}

func TestNewQuotaRefreshPending_DifferentWindowsProduceDistinctEventKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	snapshot := quotaRefreshSnapshot{SampledAt: now.Format(time.RFC3339)}
	fiveHour := newQuotaRefreshPending(12, []refreshedWindow{{Key: "five_hour"}}, snapshot, now)
	sevenDay := newQuotaRefreshPending(12, []refreshedWindow{{Key: "seven_day"}}, snapshot, now)

	assert.NotEqual(t, fiveHour.EventKey, sevenDay.EventKey)
}

func TestNewQuotaRefreshPending_SameResetProducesStableEventKey(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(5 * time.Hour)
	windows := []refreshedWindow{{Key: "five_hour", OldUtil: 98, NewUtil: 2, NewResetsAt: &reset}}
	snapshot := quotaRefreshSnapshot{SampledAt: now.Format(time.RFC3339)}

	first := newQuotaRefreshPending(12, windows, snapshot, now)
	second := newQuotaRefreshPending(12, windows, snapshot, now.Add(time.Minute))

	assert.Equal(t, first.EventKey, second.EventKey)
}

func TestQuotaRefreshListAdminEmails_PaginatesAndDeduplicatesCaseInsensitively(t *testing.T) {
	userRepo := &quotaRefreshUserRepoStub{
		pageCount: 2,
		pages: map[int][]User{
			1: {
				{Email: " Admin@Example.com "},
				{Email: ""},
			},
			2: {
				{Email: "admin@example.com"},
				{Email: "second@example.com"},
			},
		},
	}
	svc := NewQuotaRefreshNotifyService(nil, nil, userRepo, nil, nil, time.Minute)

	got := svc.listAdminEmails(context.Background())

	assert.Equal(t, []string{"Admin@Example.com", "second@example.com"}, got)
	require.Len(t, userRepo.requested, 2)
	assert.Equal(t, 1, userRepo.requested[0].Page)
	assert.Equal(t, 2, userRepo.requested[1].Page)
	assert.Equal(t, 100, userRepo.requested[0].PageSize)
	for _, filters := range userRepo.filtersSeen {
		assert.Equal(t, RoleAdmin, filters.Role)
		assert.Equal(t, StatusActive, filters.Status)
		require.NotNil(t, filters.IncludeSubscriptions)
		assert.False(t, *filters.IncludeSubscriptions)
	}
}

func TestQuotaRefreshProcessAccount_SendFailureKeepsPendingAndDoesNotConsumeSnapshot(t *testing.T) {
	now := time.Now()
	oldReset := now.Add(-time.Hour)
	newReset := now.Add(5 * time.Hour)
	account := quotaRefreshTestAccount(8, StatusActive, []string{QuotaRefreshWindowFiveHour}, map[string]usageWindowSnap{
		"five_hour": {Utilization: 97, ResetsAt: &oldReset},
	})
	usageReader := &quotaRefreshUsageReaderStub{usage: &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 1, ResetsAt: &newReset},
	}}
	repo := &quotaRefreshAccountRepoStub{}
	sender := &quotaRefreshEmailSenderStub{errors: map[string]error{"admin@example.com": assert.AnError}}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, sender, time.Minute)

	svc.processAccount(context.Background(), account, []string{"admin@example.com"}, "test")

	require.Len(t, repo.updates, 1)
	assert.Contains(t, repo.updates[0].updates, extraKeyQuotaRefreshPending)
	assert.NotContains(t, repo.updates[0].updates, extraKeyQuotaRefreshNotifySnapshot)
	assert.NotContains(t, repo.updates[0].updates, extraKeyQuotaRefreshLastNotifiedAt)
	assert.Equal(t, 1, sender.callsTo("admin@example.com"))
}

func TestQuotaRefreshProcessAccount_SameCycleMergesWindowsIntoOneEmail(t *testing.T) {
	now := time.Now()
	old5hReset := now.Add(-time.Hour)
	new5hReset := now.Add(5 * time.Hour)
	old7dReset := now.Add(-time.Hour)
	new7dReset := now.Add(7 * 24 * time.Hour)
	account := quotaRefreshTestAccount(9, StatusActive, []string{
		QuotaRefreshWindowFiveHour,
		QuotaRefreshWindowSevenDay,
	}, map[string]usageWindowSnap{
		"five_hour": {Utilization: 95, ResetsAt: &old5hReset},
		"seven_day": {Utilization: 90, ResetsAt: &old7dReset},
	})
	usageReader := &quotaRefreshUsageReaderStub{usage: &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 2, ResetsAt: &new5hReset},
		SevenDay: &UsageProgress{Utilization: 3, ResetsAt: &new7dReset},
	}}
	repo := &quotaRefreshAccountRepoStub{}
	sender := &quotaRefreshEmailSenderStub{errors: map[string]error{}}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, sender, time.Minute)

	svc.processAccount(context.Background(), account, []string{"admin@example.com"}, "test")

	require.Len(t, sender.received, 1)
	assert.Contains(t, sender.received[0].body, "5h")
	assert.Contains(t, sender.received[0].body, "7d")
	finalUpdate := repo.lastUpdate(t)
	assert.Contains(t, finalUpdate.updates, extraKeyQuotaRefreshNotifySnapshot)
	assert.Contains(t, finalUpdate.updates, extraKeyQuotaRefreshLastNotifiedAt)
	assert.Contains(t, finalUpdate.updates, extraKeyQuotaRefreshLastNotifiedWindows)
	assert.Nil(t, finalUpdate.updates[extraKeyQuotaRefreshPending])
}

func TestQuotaRefreshDeliverPending_RetriesOnlyFailedRecipients(t *testing.T) {
	now := time.Now()
	reset := now.Add(5 * time.Hour)
	pending := newQuotaRefreshPending(10, []refreshedWindow{{
		Key: "five_hour", OldUtil: 98, NewUtil: 1, NewResetsAt: &reset,
	}}, quotaRefreshSnapshot{SampledAt: now.UTC().Format(time.RFC3339), Windows: map[string]usageWindowSnap{
		"five_hour": {Utilization: 1, ResetsAt: &reset},
	}}, now)
	account := quotaRefreshTestAccount(10, StatusActive, []string{QuotaRefreshWindowFiveHour}, nil)
	repo := &quotaRefreshAccountRepoStub{}
	sender := &quotaRefreshEmailSenderStub{errors: map[string]error{"failed@example.com": assert.AnError}}
	svc := NewQuotaRefreshNotifyService(repo, nil, nil, nil, sender, time.Minute)
	recipients := []string{"sent@example.com", "failed@example.com"}

	svc.deliverPending(context.Background(), account, pending, recipients, "test")

	assert.Equal(t, []string{"sent@example.com"}, pending.Delivered)
	assert.Equal(t, 1, sender.callsTo("sent@example.com"))
	assert.Equal(t, 1, sender.callsTo("failed@example.com"))
	assert.NotContains(t, repo.lastUpdate(t).updates, extraKeyQuotaRefreshNotifySnapshot)

	sender.errors["failed@example.com"] = nil
	svc.deliverPending(context.Background(), account, pending, recipients, "test")

	assert.Equal(t, 1, sender.callsTo("sent@example.com"))
	assert.Equal(t, 2, sender.callsTo("failed@example.com"))
	assert.ElementsMatch(t, recipients, pending.Delivered)
	assert.Contains(t, repo.lastUpdate(t).updates, extraKeyQuotaRefreshNotifySnapshot)
}

func TestQuotaRefreshProcessAccount_RetriesPendingWithoutProbingUpstream(t *testing.T) {
	now := time.Now().UTC()
	pending := newQuotaRefreshPending(18, []refreshedWindow{{Key: "five_hour", OldUtil: 99, NewUtil: 1}}, quotaRefreshSnapshot{}, now)
	account := quotaRefreshTestAccount(18, StatusActive, []string{QuotaRefreshWindowFiveHour}, nil)
	account.Extra[extraKeyQuotaRefreshPending] = pending
	usageReader := &quotaRefreshUsageReaderStub{err: assert.AnError}
	repo := &quotaRefreshAccountRepoStub{}
	sender := &quotaRefreshEmailSenderStub{errors: map[string]error{}}
	svc := NewQuotaRefreshNotifyService(repo, usageReader, nil, nil, sender, time.Minute)

	svc.processAccount(context.Background(), account, []string{"admin@example.com"}, "test")

	assert.Empty(t, usageReader.calls())
	assert.Equal(t, 1, sender.callsTo("admin@example.com"))
	assert.Contains(t, repo.lastUpdate(t).updates, extraKeyQuotaRefreshNotifySnapshot)
}

func TestQuotaRefreshTemplateEmail_UsesRawHTMLOnlyForWindowTable(t *testing.T) {
	now := time.Now()
	pending := newQuotaRefreshPending(11, []refreshedWindow{{Key: "five_hour", OldUtil: 90, NewUtil: 2}}, quotaRefreshSnapshot{}, now)
	account := &Account{ID: 11, Name: "<unsafe>", Platform: PlatformAnthropic}
	templateSender := &quotaRefreshNotificationSenderStub{}
	svc := NewQuotaRefreshNotifyService(nil, nil, nil, nil, nil, time.Minute)
	svc.SetNotificationEmailService(templateSender)

	err := svc.sendQuotaRefreshEmail(context.Background(), "admin@example.com", account, pending, "test")

	require.NoError(t, err)
	assert.Equal(t, "<unsafe>", templateSender.input.Variables["account_name"])
	assert.NotContains(t, templateSender.input.Variables, "windows_html")
	assert.Contains(t, templateSender.input.RawHTMLVariables["windows_html"], "<table")
}

func TestIsWindowRefreshed_ResetsAtRolledWithUtilDrop(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(-1 * time.Hour)
	newReset := now.Add(5 * time.Hour)

	oldW := usageWindowSnap{Utilization: 95, ResetsAt: &oldReset}
	newW := usageWindowSnap{Utilization: 5, ResetsAt: &newReset}

	assert.True(t, isWindowRefreshed(oldW, newW, now))
}

func TestIsWindowRefreshed_ResetsAtRolledButUtilNotDropped(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(1 * time.Hour)
	newReset := now.Add(6 * time.Hour)

	oldW := usageWindowSnap{Utilization: 50, ResetsAt: &oldReset}
	newW := usageWindowSnap{Utilization: 48, ResetsAt: &newReset} // 下降不足 5

	assert.False(t, isWindowRefreshed(oldW, newW, now))
}

func TestIsWindowRefreshed_PastResetSignificantDrop(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(-10 * time.Minute)

	oldW := usageWindowSnap{Utilization: 90, ResetsAt: &oldReset}
	newReset := now.Add(5 * time.Hour)
	newW := usageWindowSnap{Utilization: 50, ResetsAt: &newReset} // drop 40

	assert.True(t, isWindowRefreshed(oldW, newW, now))
}

func TestIsWindowRefreshed_PastResetHighToLow(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(-5 * time.Minute)

	oldW := usageWindowSnap{Utilization: 85, ResetsAt: &oldReset}
	newReset := now.Add(4 * time.Hour)
	newW := usageWindowSnap{Utilization: 20, ResetsAt: &newReset}

	assert.True(t, isWindowRefreshed(oldW, newW, now))
}

func TestIsWindowRefreshed_SmallFluctuationNoNotify(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(2 * time.Hour)

	oldW := usageWindowSnap{Utilization: 60, ResetsAt: &oldReset}
	newW := usageWindowSnap{Utilization: 62, ResetsAt: &oldReset}

	assert.False(t, isWindowRefreshed(oldW, newW, now))
}

func TestIsWindowRefreshed_CorrectionOnlyNoNotify(t *testing.T) {
	// resets_at 微调（< 1 分钟）不应触发
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	oldReset := now.Add(3 * time.Hour)
	newReset := oldReset.Add(30 * time.Second)

	oldW := usageWindowSnap{Utilization: 70, ResetsAt: &oldReset}
	newW := usageWindowSnap{Utilization: 10, ResetsAt: &newReset}

	assert.False(t, isWindowRefreshed(oldW, newW, now))
}

func TestDetectQuotaRefresh_MultipleWindows(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	old5h := now.Add(-1 * time.Hour)
	new5h := now.Add(5 * time.Hour)
	old7d := now.Add(3 * 24 * time.Hour)

	oldWindows := map[string]usageWindowSnap{
		"five_hour": {Utilization: 99, ResetsAt: &old5h},
		"seven_day": {Utilization: 40, ResetsAt: &old7d},
	}
	newWindows := map[string]usageWindowSnap{
		"five_hour": {Utilization: 2, ResetsAt: &new5h},
		"seven_day": {Utilization: 41, ResetsAt: &old7d},
	}

	got := detectQuotaRefresh(oldWindows, newWindows, now)
	require.Len(t, got, 1)
	assert.Equal(t, "five_hour", got[0].Key)
	assert.InDelta(t, 99, got[0].OldUtil, 0.01)
	assert.InDelta(t, 2, got[0].NewUtil, 0.01)
}

func TestDetectQuotaRefresh_ColdStartEmpty(t *testing.T) {
	now := time.Now()
	newWindows := map[string]usageWindowSnap{
		"five_hour": {Utilization: 10},
	}
	assert.Nil(t, detectQuotaRefresh(nil, newWindows, now))
	assert.Nil(t, detectQuotaRefresh(map[string]usageWindowSnap{}, newWindows, now))
}

func TestExtractUsageWindows(t *testing.T) {
	reset := time.Date(2026, 7, 12, 18, 0, 0, 0, time.UTC)
	usage := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 80, ResetsAt: &reset},
		SevenDay: &UsageProgress{Utilization: 30, ResetsAt: &reset},
		AntigravityQuota: map[string]*AntigravityModelQuota{
			"claude-sonnet": {Utilization: 55, ResetTime: reset.Format(time.RFC3339)},
		},
	}
	windows := extractUsageWindows(usage)
	require.Contains(t, windows, "five_hour")
	require.Contains(t, windows, "seven_day")
	require.Contains(t, windows, "antigravity:claude-sonnet")
	assert.InDelta(t, 80, windows["five_hour"].Utilization, 0.01)
	assert.InDelta(t, 55, windows["antigravity:claude-sonnet"].Utilization, 0.01)
	require.NotNil(t, windows["antigravity:claude-sonnet"].ResetsAt)
}

func TestParseAndSnapshotRoundTrip(t *testing.T) {
	reset := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	snap := quotaRefreshSnapshot{
		SampledAt: "2026-07-12T10:00:00Z",
		Windows: map[string]usageWindowSnap{
			"five_hour": {Utilization: 88.5, ResetsAt: &reset},
		},
	}
	extra := map[string]any{
		extraKeyQuotaRefreshNotifySnapshot: snapshotToMap(snap),
	}
	parsed := parseQuotaRefreshSnapshot(extra)
	require.NotNil(t, parsed)
	assert.Equal(t, "2026-07-12T10:00:00Z", parsed.SampledAt)
	require.Contains(t, parsed.Windows, "five_hour")
	assert.InDelta(t, 88.5, parsed.Windows["five_hour"].Utilization, 0.01)
	require.NotNil(t, parsed.Windows["five_hour"].ResetsAt)
	assert.True(t, parsed.Windows["five_hour"].ResetsAt.Equal(reset))
}

func TestParseLastNotifiedAt(t *testing.T) {
	assert.Nil(t, parseLastNotifiedAt(nil))
	assert.Nil(t, parseLastNotifiedAt(map[string]any{}))

	ts := "2026-07-12T10:00:00Z"
	got := parseLastNotifiedAt(map[string]any{extraKeyQuotaRefreshLastNotifiedAt: ts})
	require.NotNil(t, got)
	assert.Equal(t, ts, got.UTC().Format(time.RFC3339))
}

func TestGetQuotaRefreshNotifyEnabled(t *testing.T) {
	assert.False(t, (*Account)(nil).GetQuotaRefreshNotifyEnabled())
	assert.False(t, (&Account{}).GetQuotaRefreshNotifyEnabled())
	assert.True(t, (&Account{Extra: map[string]any{extraKeyQuotaRefreshNotifyEnabled: true}}).GetQuotaRefreshNotifyEnabled())
	assert.False(t, (&Account{Extra: map[string]any{extraKeyQuotaRefreshNotifyEnabled: false}}).GetQuotaRefreshNotifyEnabled())
}

func TestBuildQuotaRefreshEmailBody_NoFormatErrors(t *testing.T) {
	body := buildQuotaRefreshEmailBody(42, "acct", "anthropic", "<p>ok</p>", "MySite")
	require.Contains(t, body, "MySite")
	require.Contains(t, body, "#42")
	require.Contains(t, body, "acct")
	require.Contains(t, body, "anthropic")
	require.NotContains(t, body, "%!")
	require.NotContains(t, body, "MISSING")
}

func TestFormatWindowsSummary(t *testing.T) {
	summary := formatWindowsSummary([]refreshedWindow{
		{Key: "five_hour", OldUtil: 99, NewUtil: 1},
		{Key: "seven_day", OldUtil: 80, NewUtil: 10},
	})
	assert.Contains(t, summary, "5h")
	assert.Contains(t, summary, "7d")
	assert.Contains(t, summary, "99%")
}

func TestFilterRefreshedBySelectedWindows_EmptyMeansAll(t *testing.T) {
	in := []refreshedWindow{
		{Key: "five_hour"},
		{Key: "seven_day"},
	}
	got := filterRefreshedBySelectedWindows(in, nil)
	require.Len(t, got, 2)
	got = filterRefreshedBySelectedWindows(in, []string{})
	require.Len(t, got, 2)
}

func TestFilterRefreshedBySelectedWindows_OnlyFiveHour(t *testing.T) {
	in := []refreshedWindow{
		{Key: "five_hour", OldUtil: 90, NewUtil: 1},
		{Key: "seven_day", OldUtil: 80, NewUtil: 10},
		{Key: "seven_day_sonnet", OldUtil: 70, NewUtil: 5},
	}
	got := filterRefreshedBySelectedWindows(in, []string{QuotaRefreshWindowFiveHour})
	require.Len(t, got, 1)
	assert.Equal(t, "five_hour", got[0].Key)
}

func TestFilterRefreshedBySelectedWindows_SevenDayFamily(t *testing.T) {
	in := []refreshedWindow{
		{Key: "five_hour"},
		{Key: "seven_day"},
		{Key: "seven_day_sonnet"},
	}
	got := filterRefreshedBySelectedWindows(in, []string{
		QuotaRefreshWindowSevenDay,
		QuotaRefreshWindowSevenDaySonnet,
	})
	require.Len(t, got, 2)
	keys := []string{got[0].Key, got[1].Key}
	assert.Contains(t, keys, "seven_day")
	assert.Contains(t, keys, "seven_day_sonnet")
}

func TestFilterRefreshedBySelectedWindows_AntigravityWildcard(t *testing.T) {
	in := []refreshedWindow{
		{Key: "five_hour"},
		{Key: "antigravity:claude-sonnet"},
		{Key: "antigravity:gemini-flash"},
	}
	got := filterRefreshedBySelectedWindows(in, []string{QuotaRefreshWindowAntigravityAll})
	require.Len(t, got, 2)
	for _, w := range got {
		assert.True(t, strings.HasPrefix(w.Key, "antigravity:"))
	}
}

func TestGetQuotaRefreshNotifyWindows(t *testing.T) {
	assert.Nil(t, (*Account)(nil).GetQuotaRefreshNotifyWindows())
	assert.Nil(t, (&Account{}).GetQuotaRefreshNotifyWindows())

	a := &Account{Extra: map[string]any{
		extraKeyQuotaRefreshNotifyWindows: []any{"five_hour", "seven_day", "five_hour", "  "},
	}}
	got := a.GetQuotaRefreshNotifyWindows()
	require.Equal(t, []string{"five_hour", "seven_day"}, got)

	a2 := &Account{Extra: map[string]any{
		extraKeyQuotaRefreshNotifyWindows: []string{"seven_day"},
	}}
	require.Equal(t, []string{"seven_day"}, a2.GetQuotaRefreshNotifyWindows())
}

func TestIsQuotaRefreshWindowSelected(t *testing.T) {
	assert.True(t, isQuotaRefreshWindowSelected("five_hour", nil))
	assert.True(t, isQuotaRefreshWindowSelected("five_hour", []string{"five_hour"}))
	assert.False(t, isQuotaRefreshWindowSelected("seven_day", []string{"five_hour"}))
	assert.True(t, isQuotaRefreshWindowSelected("antigravity:x", []string{"antigravity"}))
	assert.False(t, isQuotaRefreshWindowSelected("five_hour", []string{"antigravity"}))
}
