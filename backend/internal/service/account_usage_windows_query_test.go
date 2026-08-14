package service

import (
	"context"
	"math"
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

func TestDensifyWindowSamples_FillsHundredDollarAndOnePercentSteps(t *testing.T) {
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	row := usagestats.AccountUsageWindow{
		WindowStart:     start,
		WindowEnd:       end,
		SampledAt:       end,
		LastUsedPercent: 44,
		StandardCost:    437.9,
		LocalCost:       437.9,
	}
	samples := densifyWindowSamples(row, fallbackWindowSamples(row))
	require.GreaterOrEqual(t, len(samples), 40)
	require.InDelta(t, 0, samples[0].StandardCost, 0.001)
	require.InDelta(t, 0, samples[0].UsedPercent, 0.001)
	last := samples[len(samples)-1]
	require.InDelta(t, 437.9, last.StandardCost, 0.05)
	require.InDelta(t, 44, last.UsedPercent, 0.05)

	var sawHundred bool
	for _, sample := range samples {
		if math.Abs(sample.StandardCost-100) < 5 {
			sawHundred = true
			require.InDelta(t, 44*100/437.9, sample.UsedPercent, 1)
		}
	}
	require.True(t, sawHundred)
}

func TestAnnotateWindowSampleSlopes_UsesLocalDelta(t *testing.T) {
	samples := annotateWindowSampleSlopes([]usagestats.AccountWindowSample{
		{StandardCost: 0, UsedPercent: 0},
		{StandardCost: 157.8, UsedPercent: 10},
		{StandardCost: 315.6, UsedPercent: 20},
	})
	require.NotNil(t, samples[1].SlopeUSDPerPercent)
	require.InDelta(t, 15.78, *samples[1].SlopeUSDPerPercent, 0.05)
	require.NotNil(t, lastWindowSampleSlope(samples))
	require.InDelta(t, 15.78, *lastWindowSampleSlope(samples), 0.05)
}

func TestAccountUsageService_GetUsageWindows_RejectsBadType(t *testing.T) {
	svc := &AccountUsageService{windowRepo: newWindowRepoStub()}
	_, err := svc.GetUsageWindows(context.Background(), 1, time.Now().Add(-time.Hour), time.Now(), "monthly")
	require.Error(t, err)
}
