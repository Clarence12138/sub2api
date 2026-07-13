//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type bulkResetGroupRepoStub struct {
	groupRepoNoop
	groups map[int64]*Group
}

func (r bulkResetGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group, ok := r.groups[id]
	if !ok {
		return nil, errors.New("group not found")
	}
	return group, nil
}

type bulkResetUserSubRepoStub struct {
	userSubRepoNoop
	active       []UserSubscription
	resetIDs     []int64
	resetWindows QuotaResetWindows
	resetErr     error
}

type failingSubscriptionInvalidationCache struct {
	*billingCacheWorkerStub
	err error
}

func (c *failingSubscriptionInvalidationCache) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	return c.err
}

func (r *bulkResetUserSubRepoStub) ListActiveForBulkReset(context.Context, []int64, []int64, time.Time) ([]UserSubscription, error) {
	return append([]UserSubscription(nil), r.active...), nil
}

func (r *bulkResetUserSubRepoStub) ResetUsageWindowsBulk(_ context.Context, ids []int64, daily, weekly, monthly bool, _, _ time.Time) (int, error) {
	r.resetIDs = append([]int64(nil), ids...)
	r.resetWindows = QuotaResetWindows{Daily: daily, Weekly: weekly, Monthly: monthly}
	if r.resetErr != nil {
		return 0, r.resetErr
	}
	return len(ids), nil
}

func newBulkResetService(repo *bulkResetUserSubRepoStub) *SubscriptionService {
	return NewSubscriptionService(bulkResetGroupRepoStub{groups: map[int64]*Group{
		10: {ID: 10, SubscriptionType: SubscriptionTypeSubscription},
	}}, repo, nil, nil, nil)
}

func bulkResetInput() *BulkResetQuotaInput {
	return &BulkResetQuotaInput{
		Target: BulkResetQuotaTarget{
			GroupIDs: []int64{10}, SubscriptionIDs: []int64{2, 3}, ExcludedSubscriptionIDs: []int64{2},
		},
		Windows: QuotaResetWindows{Daily: true, Monthly: true},
	}
}

func TestPreviewBulkResetQuotaReportsInvalidManualTargets(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{active: []UserSubscription{
		{ID: 1, UserID: 11, GroupID: 10},
		{ID: 2, UserID: 12, GroupID: 20},
	}}

	preview, err := newBulkResetService(repo).PreviewBulkResetQuota(context.Background(), bulkResetInput())

	require.NoError(t, err)
	require.Equal(t, 2, preview.Total)
	require.Equal(t, 1, preview.Valid)
	require.Equal(t, 1, preview.Failed)
	require.Equal(t, int64(3), preview.Failures[0].SubscriptionID)
	require.Empty(t, repo.resetIDs)
}

func TestBulkResetQuotaResetsValidTargetsAndKeepsManualFailures(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{active: []UserSubscription{
		{ID: 1, UserID: 11, GroupID: 10},
		{ID: 2, UserID: 12, GroupID: 20},
	}}

	result, err := newBulkResetService(repo).BulkResetQuota(context.Background(), bulkResetInput())

	require.NoError(t, err)
	require.Equal(t, []int64{1}, repo.resetIDs)
	require.Equal(t, QuotaResetWindows{Daily: true, Monthly: true}, repo.resetWindows)
	require.Equal(t, 2, result.Total)
	require.Equal(t, 1, result.Success)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, []int64{1}, result.SuccessIDs)
	require.Equal(t, int64(3), result.Failures[0].SubscriptionID)
}

func TestBulkResetQuotaReturnsDatabaseErrorForWholeBatch(t *testing.T) {
	expected := errors.New("reset failed")
	repo := &bulkResetUserSubRepoStub{
		active:   []UserSubscription{{ID: 1, UserID: 11, GroupID: 10}},
		resetErr: expected,
	}
	input := &BulkResetQuotaInput{Target: BulkResetQuotaTarget{GroupIDs: []int64{10}}, Windows: QuotaResetWindows{Weekly: true}}

	result, err := newBulkResetService(repo).BulkResetQuota(context.Background(), input)

	require.Nil(t, result)
	require.ErrorIs(t, err, expected)
}

func TestBulkResetQuotaReportsCacheInvalidationFailure(t *testing.T) {
	repo := &bulkResetUserSubRepoStub{active: []UserSubscription{{ID: 1, UserID: 11, GroupID: 10}}}
	cacheErr := errors.New("cache unavailable")
	cache := &failingSubscriptionInvalidationCache{billingCacheWorkerStub: &billingCacheWorkerStub{}, err: cacheErr}
	svc := NewSubscriptionService(
		bulkResetGroupRepoStub{groups: map[int64]*Group{10: {ID: 10, SubscriptionType: SubscriptionTypeSubscription}}},
		repo,
		&BillingCacheService{cache: cache},
		nil,
		nil,
	)
	input := &BulkResetQuotaInput{Target: BulkResetQuotaTarget{GroupIDs: []int64{10}}, Windows: QuotaResetWindows{Daily: true}}

	result, err := svc.BulkResetQuota(context.Background(), input)

	require.NoError(t, err)
	require.Zero(t, result.Success)
	require.Equal(t, 1, result.Failed)
	require.Equal(t, int64(1), result.Failures[0].SubscriptionID)
	require.Contains(t, result.Failures[0].Error, cacheErr.Error())
}

func TestBulkResetQuotaRejectsInvalidInput(t *testing.T) {
	svc := newBulkResetService(&bulkResetUserSubRepoStub{})

	_, err := svc.PreviewBulkResetQuota(context.Background(), &BulkResetQuotaInput{Windows: QuotaResetWindows{Daily: true}})
	require.ErrorIs(t, err, ErrBulkResetTargetRequired)

	_, err = svc.PreviewBulkResetQuota(context.Background(), &BulkResetQuotaInput{
		Target: BulkResetQuotaTarget{SubscriptionIDs: []int64{0}}, Windows: QuotaResetWindows{Daily: true},
	})
	require.ErrorIs(t, err, ErrBulkResetInvalidID)

	_, err = svc.PreviewBulkResetQuota(context.Background(), &BulkResetQuotaInput{Target: BulkResetQuotaTarget{SubscriptionIDs: []int64{1}}})
	require.ErrorIs(t, err, ErrInvalidInput)
}
