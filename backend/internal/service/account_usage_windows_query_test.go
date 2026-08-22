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

func TestAnnotateWindowSampleSlopes_IntegerPercentUsesTickNotAverage(t *testing.T) {
	samples := annotateWindowSampleSlopes([]usagestats.AccountWindowSample{
		{StandardCost: 0, UsedPercent: 0},
		{StandardCost: 437.90, UsedPercent: 44},
		{StandardCost: 671.10, UsedPercent: 66},
		{StandardCost: 699.57, UsedPercent: 67},
		{StandardCost: 705.98, UsedPercent: 67},
		{StandardCost: 705.98, UsedPercent: 67},
	})
	slope := lastWindowSampleSlope(samples)
	require.NotNil(t, slope)
	require.InDelta(t, 28.47, *slope, 0.1)
	require.Greater(t, math.Abs(*slope-705.98/67), 0.5)
}

func TestAnnotateWindowSampleSlopes_StuckAtSamePercentUsesBurn(t *testing.T) {
	samples := annotateWindowSampleSlopes([]usagestats.AccountWindowSample{
		{StandardCost: 100, UsedPercent: 50},
		{StandardCost: 130, UsedPercent: 50},
	})
	slope := lastWindowSampleSlope(samples)
	require.NotNil(t, slope)
	require.InDelta(t, 30, *slope, 0.01)
}

func TestFinalizeWindowSamples_KeepsTickSlopeAfterDownsample(t *testing.T) {
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	now := start.Add(time.Hour)
	raw := make([]usagestats.AccountWindowSample, 0, 400)
	for i := 0; i < 300; i++ {
		raw = append(raw, usagestats.AccountWindowSample{
			SampledAt:    now.Add(time.Duration(i) * time.Minute),
			UsedPercent:  66,
			StandardCost: 671 + float64(i)*0.1,
		})
	}
	raw = append(raw, usagestats.AccountWindowSample{
		SampledAt:    now.Add(300 * time.Minute),
		UsedPercent:  67,
		StandardCost: 701,
	})
	for i := 0; i < 80; i++ {
		raw = append(raw, usagestats.AccountWindowSample{
			SampledAt:    now.Add(time.Duration(301+i) * time.Minute),
			UsedPercent:  67,
			StandardCost: 701 + float64(i)*0.1,
		})
	}
	last := raw[len(raw)-1]
	row := usagestats.AccountUsageWindow{
		WindowStart:     start,
		WindowEnd:       start.Add(7 * 24 * time.Hour),
		SampledAt:       last.SampledAt,
		LastUsedPercent: last.UsedPercent,
		StandardCost:    last.StandardCost,
	}
	out := finalizeWindowSamples(row, raw)
	require.LessOrEqual(t, len(out), windowSampleMaxPoints)
	slope := lastWindowSampleSlope(out)
	require.NotNil(t, slope)
	require.InDelta(t, 30, *slope, 2)
	require.Greater(t, *slope, 20.0)
}

func TestSampleFromCodexExtraIncludes7dWindowMinutes(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(7 * 24 * time.Hour)
	sample := sampleFromCodexExtra(map[string]any{
		"codex_7d_used_percent":   6.0,
		"codex_7d_reset_at":       reset.Format(time.RFC3339),
		"codex_7d_window_minutes": "10080",
	}, now, usagestats.AccountWindowClosedProbe)

	require.NotNil(t, sample.Used7dPercent)
	require.InDelta(t, 6, *sample.Used7dPercent, 0.001)
	require.NotNil(t, sample.Reset7dAt)
	require.Equal(t, reset, *sample.Reset7dAt)
	require.NotNil(t, sample.Window7dMinutes)
	require.Equal(t, 10080, *sample.Window7dMinutes)
}

func TestAccountUsageService_GetUsageWindows_RejectsBadType(t *testing.T) {
	svc := &AccountUsageService{windowRepo: newWindowRepoStub()}
	_, err := svc.GetUsageWindows(context.Background(), 1, time.Now().Add(-time.Hour), time.Now(), "monthly")
	require.Error(t, err)
	_, err = svc.GetUsageWindows(context.Background(), 1, time.Now().Add(-time.Hour), time.Now(), "5h")
	require.Error(t, err)
}
