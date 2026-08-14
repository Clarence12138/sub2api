package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	// accountWindowRollSkew 短窗（5h）新旧 reset_at 相差不超过此时长视为同一窗。
	accountWindowRollSkew = 2 * time.Minute
	// accountWindow7dRollSkew 7d 剩余秒数是 now+seconds 现算的，双源/取整常漂几分钟。
	accountWindow7dRollSkew = 30 * time.Minute
	// accountWindow7dRollMinJump 7d 占比几乎没掉时，reset_at 至少跳这么多才换窗。
	accountWindow7dRollMinJump = 6 * time.Hour
	// accountWindowResetPercentDrop 官方占比至少下降这么多，才认为窗口被重置。
	accountWindowResetPercentDrop = 3.0
	// accountWindowLowPercent 低于此峰值不反推额度。
	accountWindowLowPercent = 8.0
	// accountWindowMediumPercent 低于此峰值为中等置信。
	accountWindowMediumPercent = 20.0
	// accountWindowSlopeFlatUSD 周斜率绝对值低于此值视为持平。
	accountWindowSlopeFlatUSD          = 0.5
	accountWindowSampleMinPercentDelta = 0.3
	accountWindowSampleMinCostDelta    = 0.25
	accountWindowSampleMinInterval     = 5 * time.Minute
)

// CodexWindowSample 一次官方 5h/7d 采样。缺字段表示这次没看到该窗。
type CodexWindowSample struct {
	Used5hPercent *float64
	Reset5hAt     *time.Time
	Used7dPercent *float64
	Reset7dAt     *time.Time
	ClosedReason  string
	Now           time.Time
}

// CodexWindowObserver 在 Extra 覆盖之前观察官方窗口，避免节流丢掉换窗。
type CodexWindowObserver interface {
	Observe(ctx context.Context, accountID int64, sample CodexWindowSample)
	CloseOpenWindows(ctx context.Context, accountID int64, reason string) error
}

// AccountUsageWindowRepository 窗口快照与窗内流水聚合。
type AccountUsageWindowRepository interface {
	GetOpen(ctx context.Context, accountID int64, windowType string) (*usagestats.AccountUsageWindow, error)
	UpsertOpen(ctx context.Context, row *usagestats.AccountUsageWindow) error
	UpdateSample(ctx context.Context, id int64, peakPercent, lastPercent float64, sampledAt time.Time) error
	CloseAndOpen(ctx context.Context, closed *usagestats.AccountUsageWindow, next *usagestats.AccountUsageWindow) error
	CloseWindows(ctx context.Context, rows []*usagestats.AccountUsageWindow) error
	List(ctx context.Context, accountID int64, start, end time.Time, windowType string) ([]usagestats.AccountUsageWindow, error)
	GetLatestClosed(ctx context.Context, accountID int64, windowType string) (*usagestats.AccountUsageWindow, error)
	LastTrajectorySample(ctx context.Context, windowID int64) (*usagestats.AccountWindowSample, error)
	InsertTrajectorySample(ctx context.Context, windowID int64, sample usagestats.AccountWindowSample) error
	ListTrajectorySamples(ctx context.Context, windowIDs []int64) (map[int64][]usagestats.AccountWindowSample, error)
	SumUsage(ctx context.Context, accountID int64, start, end time.Time) (*usagestats.AccountStats, error)
	ModelUsage(ctx context.Context, accountID int64, start, end time.Time) ([]usagestats.AccountWindowModelStat, error)
	DailyModelUsage(ctx context.Context, accountID int64, start, end time.Time) ([]usagestats.AccountDailyModelStat, error)
}

func inferAccountWindowLimit(standardCost, peakPercent float64) (limit *float64, confidence string) {
	if peakPercent < accountWindowLowPercent {
		return nil, usagestats.AccountWindowConfidenceLow
	}
	if peakPercent <= 0 {
		return nil, usagestats.AccountWindowConfidenceLow
	}
	value := standardCost / (peakPercent / 100)
	limit = &value
	if peakPercent < accountWindowMediumPercent {
		return limit, usagestats.AccountWindowConfidenceMedium
	}
	return limit, usagestats.AccountWindowConfidenceHigh
}

func defaultWindowClosedReason(reason string, windowEnd, now time.Time) string {
	if reason != "" {
		return reason
	}
	if !windowEnd.IsZero() && !now.Before(windowEnd) {
		return usagestats.AccountWindowClosedExpired
	}
	return usagestats.AccountWindowClosedProbe
}

func windowDuration(windowType string) time.Duration {
	switch windowType {
	case usagestats.AccountWindowType5h:
		return 5 * time.Hour
	case usagestats.AccountWindowType7d:
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

func sameAccountWindow(prevEnd, nextEnd time.Time) bool {
	return sameAccountWindowWithin(prevEnd, nextEnd, accountWindowRollSkew)
}

func sameAccountWindowWithin(prevEnd, nextEnd time.Time, skew time.Duration) bool {
	if prevEnd.IsZero() || nextEnd.IsZero() || skew < 0 {
		return false
	}
	return absDuration(nextEnd.Sub(prevEnd)) <= skew
}

func shouldStayOnOpenWindow(open *usagestats.AccountUsageWindow, percent float64, resetAt *time.Time) bool {
	if open == nil {
		return false
	}
	if resetAt == nil {
		return true
	}
	nextEnd := resetAt.UTC()
	if open.WindowType != usagestats.AccountWindowType7d {
		return sameAccountWindow(open.WindowEnd, nextEnd)
	}
	if sameAccountWindowWithin(open.WindowEnd, nextEnd, accountWindow7dRollSkew) {
		return true
	}
	// 7d：占比没掉且 reset_at 没跳过数小时，当作剩余秒数抖动，挂在原窗。
	return !usagePercentDropped(windowUsedPercent(open), percent) &&
		!accountWindowResetJumped(open.WindowEnd, nextEnd, accountWindow7dRollMinJump)
}

func windowUsedPercent(open *usagestats.AccountUsageWindow) float64 {
	if open == nil {
		return 0
	}
	if open.PeakUsedPercent > open.LastUsedPercent {
		return open.PeakUsedPercent
	}
	return open.LastUsedPercent
}

func usagePercentDropped(prev, next float64) bool {
	return prev-next >= accountWindowResetPercentDrop
}

func accountWindowResetJumped(prev, next time.Time, minJump time.Duration) bool {
	if prev.IsZero() || next.IsZero() || minJump <= 0 {
		return false
	}
	return absDuration(next.Sub(prev)) >= minJump
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func shouldKeepTrajectorySample(prev *usagestats.AccountWindowSample, next usagestats.AccountWindowSample) bool {
	if prev == nil {
		return true
	}
	if absFloat(next.UsedPercent-prev.UsedPercent) >= accountWindowSampleMinPercentDelta {
		return true
	}
	if absFloat(next.StandardCost-prev.StandardCost) >= accountWindowSampleMinCostDelta {
		return true
	}
	return next.SampledAt.Sub(prev.SampledAt) >= accountWindowSampleMinInterval
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
