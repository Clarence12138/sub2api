package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type windowRepoStub struct {
	open       map[string]*usagestats.AccountUsageWindow
	closed     map[string]*usagestats.AccountUsageWindow
	list       []usagestats.AccountUsageWindow
	trajectory map[int64][]usagestats.AccountWindowSample
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
		open:       map[string]*usagestats.AccountUsageWindow{},
		closed:     map[string]*usagestats.AccountUsageWindow{},
		trajectory: map[int64][]usagestats.AccountWindowSample{},
		nextID:     1,
		stats:      &usagestats.AccountStats{Requests: 3, Tokens: 30, Cost: 1.2, StandardCost: 4, UserCost: 0.8},
		models:     []usagestats.AccountWindowModelStat{{Model: "gpt-5.6-luna", Requests: 2, StandardCost: 3}},
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
	closedCopy := *closed
	s.closed[closed.WindowType] = &closedCopy
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
func (s *windowRepoStub) GetLatestClosed(_ context.Context, _ int64, windowType string) (*usagestats.AccountUsageWindow, error) {
	row := s.closed[windowType]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}
func (s *windowRepoStub) LastTrajectorySample(_ context.Context, windowID int64) (*usagestats.AccountWindowSample, error) {
	items := s.trajectory[windowID]
	if len(items) == 0 {
		return nil, nil
	}
	last := items[len(items)-1]
	return &last, nil
}
func (s *windowRepoStub) InsertTrajectorySample(_ context.Context, windowID int64, sample usagestats.AccountWindowSample) error {
	s.trajectory[windowID] = append(s.trajectory[windowID], sample)
	return nil
}
func (s *windowRepoStub) ListTrajectorySamples(_ context.Context, windowIDs []int64) (map[int64][]usagestats.AccountWindowSample, error) {
	out := map[int64][]usagestats.AccountWindowSample{}
	for _, id := range windowIDs {
		out[id] = s.trajectory[id]
	}
	return out, nil
}

func TestCodexWindowRecorder_SameWindowRaisesPeak(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	used := 10.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &reset, Now: now,
	})
	used = 22.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &reset, Now: now.Add(time.Minute),
	})
	require.Equal(t, 1, repo.upserts)
	require.Equal(t, 1, repo.samples)
	require.Equal(t, 0, repo.rolls)
	require.InDelta(t, 22.0, repo.open[usagestats.AccountWindowType7d].PeakUsedPercent, 0.001)
}

func TestCodexWindowRecorder_ResetAtJumpClosesAndOpens(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	firstEnd := now.Add(2 * time.Hour)
	used := 40.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &firstEnd, Now: now,
	})
	nextEnd := firstEnd.Add(7 * 24 * time.Hour)
	used = 1.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &nextEnd, Now: firstEnd.Add(time.Minute),
	})
	require.Equal(t, 1, repo.rolls)
	open := repo.open[usagestats.AccountWindowType7d]
	require.Equal(t, nextEnd, open.WindowEnd)
	require.Equal(t, firstEnd, open.WindowStart)
	require.InDelta(t, 1.0, open.PeakUsedPercent, 0.001)
	require.NotEmpty(t, repo.trajectory[open.ID])
}

func TestCodexWindowRecorder_ResetAtJitterKeepsWindow(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 14, 7, 23, 44, 0, time.UTC)
	firstEnd := time.Date(2026, 8, 20, 3, 33, 43, 0, time.UTC)
	used := 23.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &firstEnd, Now: now,
	})
	jitterEnd := firstEnd.Add(169 * time.Second)
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &jitterEnd, Now: now.Add(17 * time.Second),
	})
	backEnd := firstEnd.Add(9 * time.Second)
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &backEnd, Now: now.Add(34 * time.Second),
	})
	require.Equal(t, 1, repo.upserts)
	require.Equal(t, 2, repo.samples)
	require.Equal(t, 0, repo.rolls)
	open := repo.open[usagestats.AccountWindowType7d]
	require.Equal(t, firstEnd, open.WindowEnd)
	require.InDelta(t, 23.0, open.LastUsedPercent, 0.001)
}

func TestCodexWindowRecorder_IgnoresResetAtNowDuringOpenWindow(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 20, 11, 33, 43, 0, time.UTC)
	end := now.Add(7 * 24 * time.Hour)
	used := 6.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &end, Now: now,
	})

	badNow := now.Add(4*time.Hour + 25*time.Minute)
	zero := 0.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &zero, Reset7dAt: &badNow, Now: badNow,
	})

	require.Equal(t, 0, repo.rolls)
	require.Equal(t, 0, repo.samples)
	require.Len(t, repo.trajectory[repo.open[usagestats.AccountWindowType7d].ID], 1)
	require.InDelta(t, 6, repo.open[usagestats.AccountWindowType7d].LastUsedPercent, 0.001)
}

func TestCodexWindowRecorder_IgnoresEarlyFutureReset(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 20, 11, 33, 43, 0, time.UTC)
	end := now.Add(7 * 24 * time.Hour)
	used := 6.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &end, Now: now,
	})

	probeNow := now.Add(2 * 24 * time.Hour)
	probeEnd := probeNow.Add(7 * 24 * time.Hour)
	zero := 0.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &zero, Reset7dAt: &probeEnd, Now: probeNow,
	})

	require.Equal(t, 0, repo.rolls)
	require.Equal(t, 0, repo.samples)
	require.Len(t, repo.trajectory[repo.open[usagestats.AccountWindowType7d].ID], 1)
	require.Equal(t, end, repo.open[usagestats.AccountWindowType7d].WindowEnd)
	require.InDelta(t, 6, repo.open[usagestats.AccountWindowType7d].LastUsedPercent, 0.001)
}

func TestCodexWindowRecorder_UpdateConflictSkipsTrajectory(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	end := now.Add(7 * 24 * time.Hour)
	used := 6.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &end, Now: now,
	})
	windowID := repo.open[usagestats.AccountWindowType7d].ID
	require.Len(t, repo.trajectory[windowID], 1)

	repo.updateErr = errors.New("stale window")
	used = 7.0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &end, Now: now.Add(time.Minute),
	})

	require.Equal(t, 1, repo.samples)
	require.Len(t, repo.trajectory[windowID], 1)
}

func TestCodexWindowRecorder_ResetOnlySampleIsIgnored(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	end := now.Add(7 * 24 * time.Hour)
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Reset7dAt: &end, Now: now,
	})

	require.Equal(t, 0, repo.upserts)
	require.Nil(t, repo.open[usagestats.AccountWindowType7d])
}

func TestCodexWindowRecorder_InvalidWindowMinutesCannotOpen(t *testing.T) {
	repo := newWindowRepoStub()
	rec := NewCodexWindowRecorder(repo)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	end := now.Add(7 * 24 * time.Hour)
	used := 0.0
	minutes := 0
	rec.Observe(context.Background(), 7, CodexWindowSample{
		Used7dPercent: &used, Reset7dAt: &end, Window7dMinutes: &minutes, Now: now,
	})

	require.Equal(t, 0, repo.upserts)
	require.Nil(t, repo.open[usagestats.AccountWindowType7d])
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
		Used7dPercent: &used, Reset7dAt: &reset, Now: now,
	})
	require.NoError(t, rec.CloseOpenWindows(context.Background(), 7, usagestats.AccountWindowClosedResetCredit))
	require.Equal(t, 1, repo.closes)
	require.Nil(t, repo.open[usagestats.AccountWindowType7d])
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
