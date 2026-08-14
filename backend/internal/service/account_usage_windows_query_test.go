package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestAccountUsageService_GetUsageWindows_EnrichesOpenRow(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	repo := newWindowRepoStub()
	open := &usagestats.AccountUsageWindow{
		ID:              3,
		AccountID:       9,
		WindowType:      usagestats.AccountWindowType7d,
		WindowStart:     now.Add(-7 * 24 * time.Hour),
		WindowEnd:       now.Add(time.Hour),
		Status:          usagestats.AccountWindowStatusOpen,
		PeakUsedPercent: 25,
	}
	require.NoError(t, repo.UpsertOpen(context.Background(), open))
	repo.list = []usagestats.AccountUsageWindow{*open}

	svc := &AccountUsageService{windowRepo: repo}
	resp, err := svc.GetUsageWindows(context.Background(), 9, now.Add(-14*24*time.Hour), now.Add(24*time.Hour), "")
	require.NoError(t, err)
	require.Len(t, resp.Windows, 1)
	require.Equal(t, usagestats.AccountWindowStatusOpen, resp.Windows[0].Status)
	require.InDelta(t, 4, resp.Windows[0].StandardCost, 0.001)
	require.NotNil(t, resp.Windows[0].InferredLimitUSD)
	require.InDelta(t, 16, *resp.Windows[0].InferredLimitUSD, 0.001)
	require.Equal(t, usagestats.AccountWindowTrendInsufficient, resp.LimitTrend.Trend)
}

func TestAccountUsageService_GetUsageWindows_RejectsBadType(t *testing.T) {
	svc := &AccountUsageService{windowRepo: newWindowRepoStub()}
	_, err := svc.GetUsageWindows(context.Background(), 1, time.Now().Add(-time.Hour), time.Now(), "monthly")
	require.Error(t, err)
}
