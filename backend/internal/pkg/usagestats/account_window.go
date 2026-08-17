package usagestats

import "time"

const (
	AccountWindowType5h = "5h"
	AccountWindowType7d = "7d"

	AccountWindowStatusOpen   = "open"
	AccountWindowStatusClosed = "closed"

	AccountWindowClosedExpired     = "expired"
	AccountWindowClosedResetCredit = "reset_credit"
	AccountWindowClosedProbe       = "probe"

	AccountWindowConfidenceHigh   = "high"
	AccountWindowConfidenceMedium = "medium"
	AccountWindowConfidenceLow    = "low"

	AccountWindowTrendLoosening    = "loosening"
	AccountWindowTrendTightening   = "tightening"
	AccountWindowTrendFlat         = "flat"
	AccountWindowTrendInsufficient = "insufficient"
)

// AccountWindowModelStat 单个模型在窗口或日期内的用量，model 保持 usage_logs.model 原值。
type AccountWindowModelStat struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	StandardCost float64 `json:"standard_cost"`
	AccountCost  float64 `json:"account_cost"`
}

// AccountUsageWindow 账号官方重置窗口快照。
type AccountUsageWindow struct {
	ID                        int64                    `json:"id"`
	AccountID                 int64                    `json:"account_id"`
	WindowType                string                   `json:"window_type"`
	WindowStart               time.Time                `json:"window_start"`
	WindowEnd                 time.Time                `json:"window_end"`
	Status                    string                   `json:"status"`
	ClosedReason              string                   `json:"closed_reason,omitempty"`
	PeakUsedPercent           float64                  `json:"peak_used_percent"`
	LastUsedPercent           float64                  `json:"last_used_percent"`
	LocalCost                 float64                  `json:"local_cost"`
	StandardCost              float64                  `json:"standard_cost"`
	UserCost                  float64                  `json:"user_cost"`
	Requests                  int64                    `json:"requests"`
	Tokens                    int64                    `json:"tokens"`
	InferredLimitUSD          *float64                 `json:"inferred_limit_usd"`
	InferredConfidence        string                   `json:"inferred_confidence"`
	ModelBreakdown            []AccountWindowModelStat `json:"model_breakdown"`
	Samples                   []AccountWindowSample    `json:"samples,omitempty"`
	CurrentSlopeUSDPerPercent *float64                 `json:"current_slope_usd_per_percent,omitempty"` // 最近 1 个官方百分点的增量 $/1%，不是总金额/总占比
	SampledAt                 time.Time                `json:"sampled_at"`
}

// AccountWindowSample 窗内一次金额×占比观测。
type AccountWindowSample struct {
	SampledAt          time.Time `json:"sampled_at"`
	UsedPercent        float64   `json:"used_percent"`
	StandardCost       float64   `json:"standard_cost"`
	LocalCost          float64   `json:"local_cost"`
	SlopeUSDPerPercent *float64  `json:"slope_usd_per_percent,omitempty"` // 到达该点时最近 1 个官方百分点的增量 $/1%
}

// AccountDailyModelStat 按自然日 × 模型原名聚合。
type AccountDailyModelStat struct {
	Date         string  `json:"date"`
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	StandardCost float64 `json:"standard_cost"`
	AccountCost  float64 `json:"account_cost"`
}

// AccountWindowLimitTrend 7d 反推额度斜率。
type AccountWindowLimitTrend struct {
	SlopeUSDPerWeek float64 `json:"slope_usd_per_week"`
	Trend           string  `json:"trend"`
	SampleCount     int     `json:"sample_count"`
}

// AccountUsageWindowsResponse 账号窗口分析接口。
type AccountUsageWindowsResponse struct {
	Windows      []AccountUsageWindow    `json:"windows"`
	DailyByModel []AccountDailyModelStat `json:"daily_by_model"`
	LimitTrend   AccountWindowLimitTrend `json:"limit_trend"`
}
