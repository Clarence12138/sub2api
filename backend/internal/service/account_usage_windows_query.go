package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// GetUsageWindows 返回账号官方窗口快照、按日模型曲线和 7d 限额斜率。
func (s *AccountUsageService) GetUsageWindows(ctx context.Context, accountID int64, start, end time.Time, windowType string) (*usagestats.AccountUsageWindowsResponse, error) {
	if s == nil || s.windowRepo == nil {
		return nil, fmt.Errorf("account usage window repository is not configured")
	}
	if end.Before(start) || end.Equal(start) {
		return nil, fmt.Errorf("invalid time range")
	}
	switch windowType {
	case "", usagestats.AccountWindowType5h, usagestats.AccountWindowType7d:
	default:
		return nil, fmt.Errorf("invalid window_type")
	}

	windows, err := s.windowRepo.List(ctx, accountID, start, end, windowType)
	if err != nil {
		return nil, fmt.Errorf("list usage windows: %w", err)
	}
	if windows == nil {
		windows = []usagestats.AccountUsageWindow{}
	}
	for i := range windows {
		if windows[i].Status != usagestats.AccountWindowStatusOpen {
			continue
		}
		if err := enrichOpenUsageWindow(ctx, s.windowRepo, &windows[i]); err != nil {
			return nil, err
		}
	}

	daily, err := s.windowRepo.DailyModelUsage(ctx, accountID, start, end)
	if err != nil {
		return nil, fmt.Errorf("daily model usage: %w", err)
	}
	if daily == nil {
		daily = []usagestats.AccountDailyModelStat{}
	}

	return &usagestats.AccountUsageWindowsResponse{
		Windows:      windows,
		DailyByModel: daily,
		LimitTrend:   buildAccountWindowLimitTrend(windows),
	}, nil
}

func enrichOpenUsageWindow(ctx context.Context, repo AccountUsageWindowRepository, row *usagestats.AccountUsageWindow) error {
	stats, err := repo.SumUsage(ctx, row.AccountID, row.WindowStart, row.WindowEnd)
	if err != nil {
		return fmt.Errorf("sum open window usage: %w", err)
	}
	models, err := repo.ModelUsage(ctx, row.AccountID, row.WindowStart, row.WindowEnd)
	if err != nil {
		return fmt.Errorf("open window model usage: %w", err)
	}
	if stats != nil {
		row.Requests = stats.Requests
		row.Tokens = stats.Tokens
		row.LocalCost = stats.Cost
		row.StandardCost = stats.StandardCost
		row.UserCost = stats.UserCost
	}
	row.ModelBreakdown = models
	limit, confidence := inferAccountWindowLimit(row.StandardCost, row.PeakUsedPercent)
	row.InferredLimitUSD = limit
	row.InferredConfidence = confidence
	return nil
}

func sampleFromCodexExtra(updates map[string]any, now time.Time, reason string) CodexWindowSample {
	sample := CodexWindowSample{Now: now, ClosedReason: reason}
	if used, ok := extraFloatPtr(updates["codex_5h_used_percent"]); ok {
		sample.Used5hPercent = used
	}
	if used, ok := extraFloatPtr(updates["codex_7d_used_percent"]); ok {
		sample.Used7dPercent = used
	}
	if resetAt, ok := extraTimePtr(updates["codex_5h_reset_at"]); ok {
		sample.Reset5hAt = resetAt
	}
	if resetAt, ok := extraTimePtr(updates["codex_7d_reset_at"]); ok {
		sample.Reset7dAt = resetAt
	}
	return sample
}

func extraFloatPtr(raw any) (*float64, bool) {
	if raw == nil {
		return nil, false
	}
	value := parseExtraFloat64(raw)
	return &value, true
}

func extraTimePtr(raw any) (*time.Time, bool) {
	if raw == nil {
		return nil, false
	}
	parsed, err := parseTime(fmt.Sprint(raw))
	if err != nil {
		return nil, false
	}
	return &parsed, true
}

func observeCodexWindow(observer CodexWindowObserver, accountID int64, sample CodexWindowSample) {
	if observer == nil || accountID <= 0 {
		return
	}
	observer.Observe(context.Background(), accountID, sample)
}
