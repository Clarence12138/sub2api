package service

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// CodexWindowRecorder 把官方 5h/7d 采样落成开窗/关窗快照。
type CodexWindowRecorder struct {
	repo AccountUsageWindowRepository
}

func NewCodexWindowRecorder(repo AccountUsageWindowRepository) *CodexWindowRecorder {
	return &CodexWindowRecorder{repo: repo}
}

func (r *CodexWindowRecorder) Observe(ctx context.Context, accountID int64, sample CodexWindowSample) {
	if r == nil || r.repo == nil || accountID <= 0 {
		return
	}
	now := sample.Now
	if now.IsZero() {
		now = time.Now()
	}
	r.observeOne(ctx, accountID, usagestats.AccountWindowType5h, sample.Used5hPercent, sample.Reset5hAt, sample.ClosedReason, now)
	r.observeOne(ctx, accountID, usagestats.AccountWindowType7d, sample.Used7dPercent, sample.Reset7dAt, sample.ClosedReason, now)
}

func (r *CodexWindowRecorder) CloseOpenWindows(ctx context.Context, accountID int64, reason string) error {
	if r == nil || r.repo == nil || accountID <= 0 {
		return nil
	}
	now := time.Now()
	var closed []*usagestats.AccountUsageWindow
	for _, windowType := range []string{usagestats.AccountWindowType5h, usagestats.AccountWindowType7d} {
		open, err := r.repo.GetOpen(ctx, accountID, windowType)
		if err != nil {
			return err
		}
		if open == nil {
			continue
		}
		if err := r.settle(ctx, open, defaultWindowClosedReason(reason, open.WindowEnd, now), now); err != nil {
			return err
		}
		closed = append(closed, open)
	}
	return r.repo.CloseWindows(ctx, closed)
}

func (r *CodexWindowRecorder) observeOne(
	ctx context.Context,
	accountID int64,
	windowType string,
	used *float64,
	resetAt *time.Time,
	reason string,
	now time.Time,
) {
	if used == nil && resetAt == nil {
		return
	}
	percent := 0.0
	if used != nil {
		percent = *used
	}
	open, err := r.repo.GetOpen(ctx, accountID, windowType)
	if err != nil {
		slog.Warn("codex_window_get_open_failed", "account_id", accountID, "window_type", windowType, "error", err)
		return
	}
	if open != nil && (resetAt == nil || sameAccountWindow(open.WindowEnd, resetAt.UTC())) {
		if err := r.repo.UpdateSample(ctx, open.ID, percent, percent, now); err != nil {
			slog.Warn("codex_window_sample_failed", "account_id", accountID, "window_type", windowType, "error", err)
		}
		r.recordTrajectory(ctx, open, percent, now)
		return
	}

	var prevEnd *time.Time
	if open != nil {
		end := open.WindowEnd
		prevEnd = &end
	} else if last, lastErr := r.repo.GetLatestClosed(ctx, accountID, windowType); lastErr != nil {
		slog.Warn("codex_window_latest_closed_failed", "account_id", accountID, "window_type", windowType, "error", lastErr)
	} else if last != nil {
		end := last.WindowEnd
		prevEnd = &end
	}
	next := buildOpenWindow(accountID, windowType, percent, resetAt, now, prevEnd)
	if next == nil {
		return
	}
	if open == nil {
		if err := r.repo.UpsertOpen(ctx, next); err != nil {
			slog.Warn("codex_window_open_failed", "account_id", accountID, "window_type", windowType, "error", err)
			return
		}
		r.recordTrajectory(ctx, next, percent, now)
		return
	}
	if err := r.settle(ctx, open, defaultWindowClosedReason(reason, open.WindowEnd, now), now); err != nil {
		slog.Warn("codex_window_settle_failed", "account_id", accountID, "window_type", windowType, "error", err)
		return
	}
	if err := r.repo.CloseAndOpen(ctx, open, next); err != nil {
		slog.Warn("codex_window_roll_failed", "account_id", accountID, "window_type", windowType, "error", err)
		return
	}
	r.recordTrajectory(ctx, next, percent, now)
}

func (r *CodexWindowRecorder) recordTrajectory(ctx context.Context, row *usagestats.AccountUsageWindow, percent float64, now time.Time) {
	if row == nil || row.ID <= 0 {
		return
	}
	end := now
	if !row.WindowEnd.IsZero() && now.After(row.WindowEnd) {
		end = row.WindowEnd
	}
	stats, err := r.repo.SumUsage(ctx, row.AccountID, row.WindowStart, end)
	if err != nil {
		slog.Warn("codex_window_trajectory_usage_failed", "window_id", row.ID, "error", err)
		return
	}
	point := usagestats.AccountWindowSample{
		SampledAt:    now,
		UsedPercent:  percent,
		StandardCost: 0,
		LocalCost:    0,
	}
	if stats != nil {
		point.StandardCost = stats.StandardCost
		point.LocalCost = stats.Cost
	}
	prev, err := r.repo.LastTrajectorySample(ctx, row.ID)
	if err != nil {
		slog.Warn("codex_window_last_sample_failed", "window_id", row.ID, "error", err)
		return
	}
	if !shouldKeepTrajectorySample(prev, point) {
		return
	}
	if err := r.repo.InsertTrajectorySample(ctx, row.ID, point); err != nil {
		slog.Warn("codex_window_insert_sample_failed", "window_id", row.ID, "error", err)
	}
}

func (r *CodexWindowRecorder) settle(ctx context.Context, row *usagestats.AccountUsageWindow, reason string, now time.Time) error {
	stats, err := r.repo.SumUsage(ctx, row.AccountID, row.WindowStart, row.WindowEnd)
	if err != nil {
		return err
	}
	models, err := r.repo.ModelUsage(ctx, row.AccountID, row.WindowStart, row.WindowEnd)
	if err != nil {
		return err
	}
	if stats != nil {
		row.Requests = stats.Requests
		row.Tokens = stats.Tokens
		row.LocalCost = stats.Cost
		row.StandardCost = stats.StandardCost
		row.UserCost = stats.UserCost
	}
	row.ModelBreakdown = models
	row.ClosedReason = reason
	row.SampledAt = now
	limit, confidence := inferAccountWindowLimit(row.StandardCost, row.PeakUsedPercent)
	row.InferredLimitUSD = limit
	row.InferredConfidence = confidence
	return nil
}

func buildOpenWindow(accountID int64, windowType string, percent float64, resetAt *time.Time, now time.Time, prevEnd *time.Time) *usagestats.AccountUsageWindow {
	duration := windowDuration(windowType)
	if duration <= 0 {
		return nil
	}
	end := now.Add(duration)
	if resetAt != nil && !resetAt.IsZero() {
		end = resetAt.UTC()
	}
	start := end.Add(-duration)
	if prevEnd != nil && !prevEnd.IsZero() && prevEnd.Before(end) {
		start = prevEnd.UTC()
	}
	if !end.After(start) {
		return nil
	}
	return &usagestats.AccountUsageWindow{
		AccountID:          accountID,
		WindowType:         windowType,
		WindowStart:        start,
		WindowEnd:          end,
		Status:             usagestats.AccountWindowStatusOpen,
		PeakUsedPercent:    percent,
		LastUsedPercent:    percent,
		InferredConfidence: usagestats.AccountWindowConfidenceLow,
		ModelBreakdown:     []usagestats.AccountWindowModelStat{},
		SampledAt:          now,
	}
}

func buildAccountWindowLimitTrend(windows []usagestats.AccountUsageWindow) usagestats.AccountWindowLimitTrend {
	points := make([][2]float64, 0, len(windows))
	for i := range windows {
		w := windows[i]
		if w.WindowType != usagestats.AccountWindowType7d {
			continue
		}
		if w.Status != usagestats.AccountWindowStatusClosed {
			continue
		}
		if w.InferredLimitUSD == nil {
			continue
		}
		if w.InferredConfidence == usagestats.AccountWindowConfidenceLow {
			continue
		}
		points = append(points, [2]float64{float64(w.WindowEnd.Unix()), *w.InferredLimitUSD})
	}
	trend := usagestats.AccountWindowLimitTrend{
		Trend:       usagestats.AccountWindowTrendInsufficient,
		SampleCount: len(points),
	}
	if len(points) < 2 {
		return trend
	}
	slopePerSecond := linearSlope(points)
	trend.SlopeUSDPerWeek = slopePerSecond * 7 * 24 * 3600
	switch {
	case trend.SlopeUSDPerWeek > accountWindowSlopeFlatUSD:
		trend.Trend = usagestats.AccountWindowTrendLoosening
	case trend.SlopeUSDPerWeek < -accountWindowSlopeFlatUSD:
		trend.Trend = usagestats.AccountWindowTrendTightening
	default:
		trend.Trend = usagestats.AccountWindowTrendFlat
	}
	return trend
}

func linearSlope(points [][2]float64) float64 {
	n := float64(len(points))
	var sumX, sumY, sumXY, sumXX float64
	for _, p := range points {
		sumX += p[0]
		sumY += p[1]
		sumXY += p[0] * p[1]
		sumXX += p[0] * p[0]
	}
	denom := n*sumXX - sumX*sumX
	if math.Abs(denom) < 1e-9 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}
