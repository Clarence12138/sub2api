package service

import (
	"context"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	// accountWindowRollSkew 短窗（5h）新旧 reset_at 相差不超过此时长视为同一窗。
	accountWindowRollSkew = 2 * time.Minute
	// accountWindow7dRollSkew 7d 剩余秒数是 now+seconds 现算的，双源/取整常漂几分钟。
	accountWindow7dRollSkew = 30 * time.Minute
	// 官方 7d 时长偶尔会有取整差异，允许 6d-8d，拒绝 0 等占位窗口。
	accountWindow7dMinMinutes = 6 * 24 * 60
	accountWindow7dMaxMinutes = 8 * 24 * 60
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
	Used5hPercent   *float64
	Reset5hAt       *time.Time
	Used7dPercent   *float64
	Reset7dAt       *time.Time
	Window7dMinutes *int
	ClosedReason    string
	Now             time.Time
}

type accountWindowSampleAction uint8

const (
	accountWindowSampleUpdate accountWindowSampleAction = iota
	accountWindowSampleIgnore
	accountWindowSampleRoll
)

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

func classifyAccountWindowSample(open *usagestats.AccountUsageWindow, resetAt *time.Time, now time.Time) accountWindowSampleAction {
	if open == nil {
		return accountWindowSampleRoll
	}
	if resetAt == nil {
		return accountWindowSampleUpdate
	}
	nextEnd := resetAt.UTC()
	if open.WindowType != usagestats.AccountWindowType7d {
		if sameAccountWindow(open.WindowEnd, nextEnd) {
			return accountWindowSampleUpdate
		}
		return accountWindowSampleRoll
	}
	if sameAccountWindowWithin(open.WindowEnd, nextEnd, accountWindow7dRollSkew) {
		return accountWindowSampleUpdate
	}
	// 普通 probe 不能在官方窗口结束前提前换窗。主动 credit reset
	// 走 CloseOpenWindows 的显式路径，不依赖这里的启发式判断。
	if now.Before(open.WindowEnd) {
		return accountWindowSampleIgnore
	}
	// 新 reset 必须明确落在当前窗口之后；旧值、倒退值都视为陈旧样本。
	if !nextEnd.After(open.WindowEnd.Add(accountWindow7dRollSkew)) {
		return accountWindowSampleIgnore
	}
	return accountWindowSampleRoll
}

func validAccountWindowSample(windowType string, used *float64, resetAt *time.Time, windowMinutes *int, now time.Time, opening bool) bool {
	if used == nil {
		return false
	}
	if math.IsNaN(*used) || math.IsInf(*used, 0) || *used < 0 || *used > 100 {
		return false
	}
	if windowType == usagestats.AccountWindowType7d && windowMinutes != nil {
		if *windowMinutes < accountWindow7dMinMinutes || *windowMinutes > accountWindow7dMaxMinutes {
			return false
		}
	}
	if resetAt != nil {
		end := resetAt.UTC()
		maxDuration := windowDuration(windowType)
		if windowMinutes != nil {
			maxDuration = time.Duration(*windowMinutes) * time.Minute
		}
		if !end.After(now) || (maxDuration > 0 && end.After(now.Add(maxDuration+accountWindow7dRollSkew))) {
			return false
		}
	}
	// 没有 reset_at 时，首次观测无法建立稳定的窗口身份。
	return !opening || resetAt != nil
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
