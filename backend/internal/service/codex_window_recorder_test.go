package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type windowRepoStub struct {
	open       map[string]*usagestats.AccountUsageWindow
	list       []usagestats.AccountUsageWindow
	upserts    int
	samples    int
	rolls      int
	closes     int
	nextID     int64
	stats      *usagestats.AccountStats
	models     []usagestats.AccountWindowModelStat
	getOpenErr error
	updateErr  error
	upsertErr  error
	rollErr    error
}

func newWindowRepoStub() *windowRepoStub {
	return &windowRepoStub{
		open:   map[string]*usagestats.AccountUsageWindow{},
		nextID: 1,
		stats:  &usagestats.AccountStats{Requests: 3, Tokens: 30, Cost: 1.2, StandardCost: 4, UserCost: 0.8},
		models: []usagestats.AccountWindowModelStat{{Model: "gpt-5.6-luna", Requests: 2, StandardCost: 3}},
	}
}

func (s *windowRepoStub) GetOpen(_ context.Context, _ int64, windowType string) (*usagestats.AccountUsageWindow, error) {
	if s.getOpenErr != nil {
		return nil, s.getOpenErr
	}
	row := s.open[windowType]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (s *windowRepoStub) UpsertOpen(_ context.Context, row *usagestats.AccountUsageWindow) error {
	s.upserts++
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if row.ID == 0 {
		row.ID = s.nextID
		s.nextID++
	}
	clone := *row
	s.open[row.WindowType] = &clone
	return nil
}

func (s *windowRepoStub) UpdateSample(_ context.Context, id int64, peakPercent, lastPercent float64, sampledAt time.Time) error {
	s.samples++
	if s.updateErr != nil {
		return s.updateErr
	}
	for _, row := range s.open {
		if row.ID == id {
			if peakPercent > row.PeakUsedPercent {
				row.PeakUsedPercent = peakPercent
			}
			row.LastUsedPercent = lastPercent
			row.SampledAt = sampledAt
		}
	}
	return nil
}

func (s *windowRepoStub) CloseAndOpen(_ context.Context, closed *usagestats.AccountUsageWindow, next *usagestats.AccountUsageWindow) error {
	s.rolls++
	if s.rollErr != nil {
		return s.rollErr
	}
	delete(s.open, closed.WindowType)
	if next.ID == 0 {
		next.ID = s.nextID
		s.nextID++
	}
	clone := *next
	s.open[next.WindowType] = &clone
	return nil
}

func (s *windowRepoStub) CloseWindows(_ context.Context, rows []*usagestats.AccountUsageWindow) error {
	s.closes++
	for _, row := range rows {
		delete(s.open, row.WindowType)
	}
	return nil
}

func (s *windowRepoStub) List(context.Context, int64, time.Time, time.Time, string) ([]usagestats.AccountUsageWindow, error) {
	if s.list == nil {
		return []usagestats.AccountUsageWindow{}, nil
	}
	return s.list, nil
}
func (s *windowRepoStub) SumUsage(context.Context, int64, time.Time, time.Time) (*usagestats.AccountStats, error) {
	return s.stats, nil
}
func (s *windowRepoStub) ModelUsage(context.Context, int64, time.Time, time.Time) ([]usagestats.AccountWindowModelStat, error) {
	return s.models, nil
}
func (s *windowRepoStub) DailyModelUsage(context.Context, int64, time.Time, time.Time) ([]usagestats.AccountDailyModelStat, error) {
	return nil, nil
}

func TestCodexWindowRecorder_SameWindowRaisesPeak(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	used := 10.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used5hPercent: &used, Reset5hAt: &reset, Now: now,
	})
	used = 22.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used5hPercent: &used, Reset5hAt: &reset, Now: now.Add(time.Minute),
	})
	require.Equal(t, 1, repo.upserts)
	require.Equal(t, 1, repo.samples)
	require.Equal(t, 0, repo.rolls)
	require.InDelta(t, 22.0, repo.open[usagestats.AccountWindowType5h].PeakUsedPercent, 0.001)
}

func TestCodexWindowRecorder_ResetAtJumpClosesAndOpens(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	firstEnd := now.Add(2 * time.Hour)
	used := 40.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used5hPercent: &used, Reset5hAt: &firstEnd, Now: now,
	})
	nextEnd := firstEnd.Add(5 * time.Hour)
	used = 1.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used5hPercent: &used, Reset5hAt: &nextEnd, Now: firstEnd.Add(time.Minute),
	})
	require.Equal(t, 1, repo.rolls)
	open := repo.open[usagestats.AccountWindowType5h]
	require.Equal(t, nextEnd, open.WindowEnd)
	require.InDelta(t, 1.0, open.PeakUsedPercent, 0.001)
}

func TestInferAccountWindowLimit(t *testing.T) {
	limit, conf := inferAccountWindowLimit(8, 4)
	require.Nil(t, limit)
	require.Equal(t, usagestats.AccountWindowConfidenceLow, conf)

	limit, conf = inferAccountWindowLimit(8, 10)
	require.NotNil(t, limit)
	require.InDelta(t, 80, *limit, 0.001)
	require.Equal(t, usagestats.AccountWindowConfidenceMedium, conf)

	limit, conf = inferAccountWindowLimit(12, 40)
	require.NotNil(t, limit)
	require.InDelta(t, 30, *limit, 0.001)
	require.Equal(t, usagestats.AccountWindowConfidenceHigh, conf)
}

func TestCodexWindowRecorder_CloseOpenWindowsSettles(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	used := 40.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used5hPercent: &used, Reset5hAt: &reset, Now: now,
	})
	require.NoError(t, rec.CloseOpenWindows(context.Background(), 7, usagestats.AccountWindowClosedResetCredit))
	require.Equal(t, 1, repo.closes)
	require.Nil(t, repo.open[usagestats.AccountWindowType5h])
}

func TestBuildAccountWindowLimitTrend(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	limit20, limit30 := 20.0, 30.0
	windows := []usagestats.AccountUsageWindow{
		{
			WindowType: usagestats.AccountWindowType7d, Status: usagestats.AccountWindowStatusClosed,
			WindowEnd: base, InferredLimitUSD: &limit20, InferredConfidence: usagestats.AccountWindowConfidenceHigh,
		},
		{
			WindowType: usagestats.AccountWindowType7d, Status: usagestats.AccountWindowStatusClosed,
			WindowEnd: base.Add(7 * 24 * time.Hour), InferredLimitUSD: &limit30, InferredConfidence: usagestats.AccountWindowConfidenceHigh,
		},
	}
	trend := buildAccountWindowLimitTrend(windows)
	require.Equal(t, 2, trend.SampleCount)
	require.Equal(t, usagestats.AccountWindowTrendLoosening, trend.Trend)
	require.InDelta(t, 10, trend.SlopeUSDPerWeek, 0.01)

	require.Equal(t, usagestats.AccountWindowTrendInsufficient, buildAccountWindowLimitTrend(nil).Trend)
}
