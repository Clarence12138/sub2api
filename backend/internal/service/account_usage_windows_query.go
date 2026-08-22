package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const (
	windowSampleCostStep    = 100.0
	windowSamplePercentStep = 1.0
	windowSampleMaxPoints   = 200
	// 官方占比多为整数。相邻采样经常停在同一个百分点上，
	// 斜率必须跨过至少半个百分点，不能用「总金额 / 总占比」。
	windowSlopeMinPercentDelta = 0.5
)

// GetUsageWindows 返回账号官方窗口快照、按日模型曲线和 7d 限额斜率。
func (s *AccountUsageService) GetUsageWindows(ctx context.Context, accountID int64, start, end time.Time, windowType string) (*usagestats.AccountUsageWindowsResponse, error) {
	if s == nil || s.windowRepo == nil {
		return nil, fmt.Errorf("account usage window repository is not configured")
	}
	if end.Before(start) || end.Equal(start) {
		return nil, fmt.Errorf("invalid time range")
	}
	if windowType == "" {
		windowType = usagestats.AccountWindowType7d
	}
	if windowType != usagestats.AccountWindowType7d {
		return nil, fmt.Errorf("invalid window_type")
	}

	windows, err := s.windowRepo.List(ctx, accountID, start, end, windowType)
	if err != nil {
		return nil, fmt.Errorf("list usage windows: %w", err)
	}
	if windows == nil {
		windows = []usagestats.AccountUsageWindow{}
	}
	ids := make([]int64, 0, len(windows))
	for i := range windows {
		if windows[i].Status == usagestats.AccountWindowStatusOpen {
			if err := enrichOpenUsageWindow(ctx, s.windowRepo, &windows[i]); err != nil {
				return nil, err
			}
		}
		if windows[i].ID > 0 {
			ids = append(ids, windows[i].ID)
		}
	}
	samplesByWindow, err := s.windowRepo.ListTrajectorySamples(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list window samples: %w", err)
	}
	for i := range windows {
		samples := finalizeWindowSamples(windows[i], samplesByWindow[windows[i].ID])
		windows[i].Samples = samples
		windows[i].CurrentSlopeUSDPerPercent = lastWindowSampleSlope(samples)
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

func fallbackWindowSamples(row usagestats.AccountUsageWindow) []usagestats.AccountWindowSample {
	percent := row.LastUsedPercent
	if percent <= 0 {
		percent = row.PeakUsedPercent
	}
	if percent <= 0 && row.StandardCost <= 0 {
		return []usagestats.AccountWindowSample{}
	}
	sampledAt := row.SampledAt
	if sampledAt.IsZero() {
		sampledAt = row.WindowEnd
	}
	return []usagestats.AccountWindowSample{{
		SampledAt:    sampledAt,
		UsedPercent:  percent,
		StandardCost: row.StandardCost,
		LocalCost:    row.LocalCost,
	}}
}

func densifyWindowSamples(row usagestats.AccountUsageWindow, samples []usagestats.AccountWindowSample) []usagestats.AccountWindowSample {
	waypoints := make([]usagestats.AccountWindowSample, 0, len(samples)+2)
	waypoints = append(waypoints, usagestats.AccountWindowSample{SampledAt: row.WindowStart})
	sorted := append([]usagestats.AccountWindowSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].SampledAt.Equal(sorted[j].SampledAt) {
			return sorted[i].StandardCost < sorted[j].StandardCost
		}
		return sorted[i].SampledAt.Before(sorted[j].SampledAt)
	})
	for _, sample := range sorted {
		if sample.StandardCost < 0 {
			sample.StandardCost = 0
		}
		if sample.UsedPercent < 0 {
			sample.UsedPercent = 0
		}
		waypoints = append(waypoints, sample)
	}
	endPercent := row.LastUsedPercent
	if endPercent <= 0 {
		endPercent = row.PeakUsedPercent
	}
	if row.StandardCost > 0 || endPercent > 0 {
		end := usagestats.AccountWindowSample{
			SampledAt:    row.SampledAt,
			UsedPercent:  endPercent,
			StandardCost: row.StandardCost,
			LocalCost:    row.LocalCost,
		}
		if end.SampledAt.IsZero() {
			end.SampledAt = row.WindowEnd
		}
		last := waypoints[len(waypoints)-1]
		if absFloat(end.StandardCost-last.StandardCost) > 0.01 || absFloat(end.UsedPercent-last.UsedPercent) > 0.05 {
			waypoints = append(waypoints, end)
		}
	}

	out := make([]usagestats.AccountWindowSample, 0, 32)
	out = append(out, waypoints[0])
	for i := 1; i < len(waypoints); i++ {
		out = append(out, interpolateWindowSegment(waypoints[i-1], waypoints[i])...)
		out = append(out, waypoints[i])
	}
	return out
}

func finalizeWindowSamples(row usagestats.AccountUsageWindow, samples []usagestats.AccountWindowSample) []usagestats.AccountWindowSample {
	if len(samples) == 0 {
		samples = fallbackWindowSamples(row)
	}
	out := annotateWindowSampleSlopes(densifyWindowSamples(row, samples))
	if len(out) > windowSampleMaxPoints {
		out = downsampleWindowSamples(out, windowSampleMaxPoints)
	}
	return out
}

func interpolateWindowSegment(start, end usagestats.AccountWindowSample) []usagestats.AccountWindowSample {
	dc := end.StandardCost - start.StandardCost
	dp := end.UsedPercent - start.UsedPercent
	if dc <= 0 && dp <= 0 {
		return nil
	}
	steps := int(math.Max(math.Ceil(math.Abs(dc)/windowSampleCostStep), math.Ceil(math.Abs(dp)/windowSamplePercentStep)))
	if steps < 2 {
		return nil
	}
	out := make([]usagestats.AccountWindowSample, 0, steps-1)
	span := end.SampledAt.Sub(start.SampledAt)
	for i := 1; i < steps; i++ {
		t := float64(i) / float64(steps)
		out = append(out, usagestats.AccountWindowSample{
			SampledAt:    start.SampledAt.Add(time.Duration(float64(span) * t)),
			UsedPercent:  start.UsedPercent + dp*t,
			StandardCost: start.StandardCost + dc*t,
			LocalCost:    start.LocalCost + (end.LocalCost-start.LocalCost)*t,
		})
	}
	return out
}

func annotateWindowSampleSlopes(samples []usagestats.AccountWindowSample) []usagestats.AccountWindowSample {
	firstIdx := make(map[int]int, len(samples))
	for i := range samples {
		level := windowPercentLevel(samples[i].UsedPercent)
		if _, exists := firstIdx[level]; !exists {
			firstIdx[level] = i
		}
		if slope, ok := incrementalSlopeAtLevel(samples, firstIdx, i, level); ok {
			value := slope
			samples[i].SlopeUSDPerPercent = &value
		}
	}
	return samples
}

func windowPercentLevel(percent float64) int {
	if percent < 0 {
		return 0
	}
	return int(math.Floor(percent + 1e-9))
}

// incrementalSlopeAtLevel 用「上一个百分点首次出现 → 当前百分点首次出现」的增量。
// 同一整数占比上继续烧钱时，已花费可作为下一百分点的下限。
func incrementalSlopeAtLevel(
	samples []usagestats.AccountWindowSample,
	firstIdx map[int]int,
	index int,
	level int,
) (float64, bool) {
	currFirst, ok := firstIdx[level]
	if !ok {
		return 0, false
	}
	completed, hasCompleted := completedLevelSlope(samples, firstIdx, currFirst, level)
	burned := samples[index].StandardCost - samples[currFirst].StandardCost
	if burned < 0 {
		burned = 0
	}
	switch {
	case hasCompleted && burned > completed:
		return burned, true
	case hasCompleted:
		return completed, true
	case burned > 0:
		return burned, true
	default:
		return 0, false
	}
}

func completedLevelSlope(
	samples []usagestats.AccountWindowSample,
	firstIdx map[int]int,
	currFirst int,
	level int,
) (float64, bool) {
	for prev := level - 1; prev >= 0; prev-- {
		prevFirst, ok := firstIdx[prev]
		if !ok {
			continue
		}
		dp := samples[currFirst].UsedPercent - samples[prevFirst].UsedPercent
		dc := samples[currFirst].StandardCost - samples[prevFirst].StandardCost
		if dp >= windowSlopeMinPercentDelta && dc >= 0 {
			return dc / dp, true
		}
		return 0, false
	}
	return 0, false
}

func lastWindowSampleSlope(samples []usagestats.AccountWindowSample) *float64 {
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].SlopeUSDPerPercent != nil {
			value := *samples[i].SlopeUSDPerPercent
			return &value
		}
	}
	return nil
}

func downsampleWindowSamples(samples []usagestats.AccountWindowSample, limit int) []usagestats.AccountWindowSample {
	if len(samples) <= limit || limit < 2 {
		return samples
	}
	out := make([]usagestats.AccountWindowSample, 0, limit)
	out = append(out, samples[0])
	step := float64(len(samples)-1) / float64(limit-1)
	for i := 1; i < limit-1; i++ {
		out = append(out, samples[int(math.Round(float64(i)*step))])
	}
	out = append(out, samples[len(samples)-1])
	return out
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
	if minutes, ok := extraIntPtr(updates["codex_7d_window_minutes"]); ok {
		sample.Window7dMinutes = minutes
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

func extraIntPtr(raw any) (*int, bool) {
	if raw == nil {
		return nil, false
	}
	value := parseExtraInt(raw)
	return &value, true
}

func observeCodexWindow(observer CodexWindowObserver, accountID int64, sample CodexWindowSample) {
	if observer == nil || accountID <= 0 {
		return
	}
	observer.Observe(context.Background(), accountID, sample)
}
