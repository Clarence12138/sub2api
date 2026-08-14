package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type accountUsageWindowRepository struct {
	sql sqlExecutor
	db  *sql.DB
}

func NewAccountUsageWindowRepository(sqlDB *sql.DB) service.AccountUsageWindowRepository {
	return &accountUsageWindowRepository{sql: sqlDB, db: sqlDB}
}

func (r *accountUsageWindowRepository) GetOpen(ctx context.Context, accountID int64, windowType string) (*usagestats.AccountUsageWindow, error) {
	row, err := r.scanOne(ctx, r.sql, `
		SELECT id, account_id, window_type, window_start, window_end, status,
			COALESCE(closed_reason, ''), peak_used_percent, last_used_percent,
			local_cost, standard_cost, user_cost, requests, tokens,
			inferred_limit_usd, inferred_confidence, model_breakdown, sampled_at
		FROM account_usage_windows
		WHERE account_id = $1 AND window_type = $2 AND status = 'open'
	`, accountID, windowType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func (r *accountUsageWindowRepository) UpsertOpen(ctx context.Context, row *usagestats.AccountUsageWindow) error {
	return upsertOpenWith(ctx, r.sql, row)
}

func (r *accountUsageWindowRepository) UpdateSample(ctx context.Context, id int64, peakPercent, lastPercent float64, sampledAt time.Time) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE account_usage_windows
		SET peak_used_percent = GREATEST(peak_used_percent, $2),
			last_used_percent = $3,
			sampled_at = $4,
			updated_at = NOW()
		WHERE id = $1 AND status = 'open'
	`, id, peakPercent, lastPercent, sampledAt)
	return err
}

func (r *accountUsageWindowRepository) CloseAndOpen(ctx context.Context, closed *usagestats.AccountUsageWindow, next *usagestats.AccountUsageWindow) error {
	if r.db == nil {
		return r.closeAndOpenWith(ctx, r.sql, closed, next)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.closeAndOpenWith(ctx, tx, closed, next); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountUsageWindowRepository) closeAndOpenWith(ctx context.Context, exec sqlExecutor, closed *usagestats.AccountUsageWindow, next *usagestats.AccountUsageWindow) error {
	if err := r.closeOne(ctx, exec, closed); err != nil {
		return err
	}
	return upsertOpenWith(ctx, exec, next)
}

func (r *accountUsageWindowRepository) CloseWindows(ctx context.Context, rows []*usagestats.AccountUsageWindow) error {
	if len(rows) == 0 {
		return nil
	}
	if r.db == nil {
		for _, row := range rows {
			if err := r.closeOne(ctx, r.sql, row); err != nil {
				return err
			}
		}
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range rows {
		if err := r.closeOne(ctx, tx, row); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *accountUsageWindowRepository) closeOne(ctx context.Context, exec sqlExecutor, row *usagestats.AccountUsageWindow) error {
	if row == nil || row.ID <= 0 {
		return nil
	}
	breakdown, err := json.Marshal(row.ModelBreakdown)
	if err != nil {
		return err
	}
	if row.ModelBreakdown == nil {
		breakdown = []byte("[]")
	}
	var reason any
	if row.ClosedReason != "" {
		reason = row.ClosedReason
	}
	_, err = exec.ExecContext(ctx, `
		UPDATE account_usage_windows
		SET status = 'closed',
			closed_reason = $2,
			peak_used_percent = $3,
			last_used_percent = $4,
			local_cost = $5,
			standard_cost = $6,
			user_cost = $7,
			requests = $8,
			tokens = $9,
			inferred_limit_usd = $10,
			inferred_confidence = $11,
			model_breakdown = $12,
			sampled_at = $13,
			updated_at = NOW()
		WHERE id = $1 AND status = 'open'
	`, row.ID, reason, row.PeakUsedPercent, row.LastUsedPercent,
		row.LocalCost, row.StandardCost, row.UserCost, row.Requests, row.Tokens,
		row.InferredLimitUSD, row.InferredConfidence, breakdown, row.SampledAt)
	return err
}

func upsertOpenWith(ctx context.Context, exec sqlExecutor, row *usagestats.AccountUsageWindow) error {
	if row == nil {
		return nil
	}
	breakdown, err := json.Marshal(row.ModelBreakdown)
	if err != nil {
		return err
	}
	if row.ModelBreakdown == nil {
		breakdown = []byte("[]")
	}
	var id int64
	err = scanSingleRow(ctx, exec, `
		INSERT INTO account_usage_windows (
			account_id, window_type, window_start, window_end, status,
			peak_used_percent, last_used_percent, inferred_confidence,
			model_breakdown, sampled_at
		) VALUES ($1,$2,$3,$4,'open',$5,$6,'low',$7,$8)
		ON CONFLICT (account_id, window_type, window_end) DO UPDATE SET
			peak_used_percent = GREATEST(account_usage_windows.peak_used_percent, EXCLUDED.peak_used_percent),
			last_used_percent = EXCLUDED.last_used_percent,
			sampled_at = EXCLUDED.sampled_at,
			updated_at = NOW()
		WHERE account_usage_windows.status = 'open'
		RETURNING id
	`, []any{row.AccountID, row.WindowType, row.WindowStart, row.WindowEnd,
		row.PeakUsedPercent, row.LastUsedPercent, breakdown, row.SampledAt}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	row.ID = id
	return nil
}

func (r *accountUsageWindowRepository) GetLatestClosed(ctx context.Context, accountID int64, windowType string) (*usagestats.AccountUsageWindow, error) {
	row, err := r.scanOne(ctx, r.sql, `
		SELECT id, account_id, window_type, window_start, window_end, status,
			COALESCE(closed_reason, ''), peak_used_percent, last_used_percent,
			local_cost, standard_cost, user_cost, requests, tokens,
			inferred_limit_usd, inferred_confidence, model_breakdown, sampled_at
		FROM account_usage_windows
		WHERE account_id = $1 AND window_type = $2 AND status = 'closed'
		ORDER BY window_end DESC
		LIMIT 1
	`, accountID, windowType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func (r *accountUsageWindowRepository) LastTrajectorySample(ctx context.Context, windowID int64) (*usagestats.AccountWindowSample, error) {
	if windowID <= 0 {
		return nil, nil
	}
	var sample usagestats.AccountWindowSample
	err := scanSingleRow(ctx, r.sql, `
		SELECT sampled_at, used_percent, standard_cost, local_cost
		FROM account_usage_window_samples
		WHERE window_id = $1
		ORDER BY sampled_at DESC, id DESC
		LIMIT 1
	`, []any{windowID}, &sample.SampledAt, &sample.UsedPercent, &sample.StandardCost, &sample.LocalCost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sample, nil
}

func (r *accountUsageWindowRepository) InsertTrajectorySample(ctx context.Context, windowID int64, sample usagestats.AccountWindowSample) error {
	if windowID <= 0 {
		return nil
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO account_usage_window_samples (window_id, sampled_at, used_percent, standard_cost, local_cost)
		VALUES ($1,$2,$3,$4,$5)
	`, windowID, sample.SampledAt, sample.UsedPercent, sample.StandardCost, sample.LocalCost)
	return err
}

func (r *accountUsageWindowRepository) ListTrajectorySamples(ctx context.Context, windowIDs []int64) (map[int64][]usagestats.AccountWindowSample, error) {
	out := make(map[int64][]usagestats.AccountWindowSample, len(windowIDs))
	if len(windowIDs) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT window_id, sampled_at, used_percent, standard_cost, local_cost
		FROM account_usage_window_samples
		WHERE window_id = ANY($1)
		ORDER BY window_id ASC, sampled_at ASC, id ASC
	`, pq.Array(windowIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			windowID int64
			sample   usagestats.AccountWindowSample
		)
		if err := rows.Scan(&windowID, &sample.SampledAt, &sample.UsedPercent, &sample.StandardCost, &sample.LocalCost); err != nil {
			return nil, err
		}
		out[windowID] = append(out[windowID], sample)
	}
	return out, rows.Err()
}

func (r *accountUsageWindowRepository) List(ctx context.Context, accountID int64, start, end time.Time, windowType string) ([]usagestats.AccountUsageWindow, error) {
	query := `
		SELECT id, account_id, window_type, window_start, window_end, status,
			COALESCE(closed_reason, ''), peak_used_percent, last_used_percent,
			local_cost, standard_cost, user_cost, requests, tokens,
			inferred_limit_usd, inferred_confidence, model_breakdown, sampled_at
		FROM account_usage_windows
		WHERE account_id = $1 AND window_end > $2 AND window_start < $3
	`
	args := []any{accountID, start, end}
	if windowType != "" {
		query += " AND window_type = $4"
		args = append(args, windowType)
	}
	query += " ORDER BY window_end ASC, window_type ASC"
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]usagestats.AccountUsageWindow, 0)
	for rows.Next() {
		row, err := scanAccountUsageWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func (r *accountUsageWindowRepository) SumUsage(ctx context.Context, accountID int64, start, end time.Time) (*usagestats.AccountStats, error) {
	stats := &usagestats.AccountStats{}
	err := scanSingleRow(ctx, r.sql, `
		SELECT
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE account_id = $1 AND created_at >= $2 AND created_at < $3
	`, []any{accountID, start, end},
		&stats.Requests, &stats.Tokens, &stats.Cost, &stats.StandardCost, &stats.UserCost)
	if err != nil {
		return nil, err
	}
	return stats, nil
}

func (r *accountUsageWindowRepository) ModelUsage(ctx context.Context, accountID int64, start, end time.Time) ([]usagestats.AccountWindowModelStat, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT model,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as account_cost
		FROM usage_logs
		WHERE account_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY model
		ORDER BY standard_cost DESC, model ASC
	`, accountID, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]usagestats.AccountWindowModelStat, 0)
	for rows.Next() {
		var item usagestats.AccountWindowModelStat
		if err := rows.Scan(&item.Model, &item.Requests, &item.Tokens, &item.StandardCost, &item.AccountCost); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *accountUsageWindowRepository) DailyModelUsage(ctx context.Context, accountID int64, start, end time.Time) ([]usagestats.AccountDailyModelStat, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT TO_CHAR(created_at, 'YYYY-MM-DD') as date,
			model,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as account_cost
		FROM usage_logs
		WHERE account_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY date, model
		ORDER BY date ASC, standard_cost DESC, model ASC
	`, accountID, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]usagestats.AccountDailyModelStat, 0)
	for rows.Next() {
		var item usagestats.AccountDailyModelStat
		if err := rows.Scan(&item.Date, &item.Model, &item.Requests, &item.Tokens, &item.StandardCost, &item.AccountCost); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type windowScanner interface {
	Scan(dest ...any) error
}

func (r *accountUsageWindowRepository) scanOne(ctx context.Context, q sqlQueryer, query string, args ...any) (*usagestats.AccountUsageWindow, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	row, err := scanAccountUsageWindow(rows)
	if err != nil {
		return nil, err
	}
	return row, rows.Err()
}

func scanAccountUsageWindow(s windowScanner) (*usagestats.AccountUsageWindow, error) {
	var (
		row       usagestats.AccountUsageWindow
		limit     sql.NullFloat64
		breakdown []byte
	)
	if err := s.Scan(
		&row.ID, &row.AccountID, &row.WindowType, &row.WindowStart, &row.WindowEnd, &row.Status,
		&row.ClosedReason, &row.PeakUsedPercent, &row.LastUsedPercent,
		&row.LocalCost, &row.StandardCost, &row.UserCost, &row.Requests, &row.Tokens,
		&limit, &row.InferredConfidence, &breakdown, &row.SampledAt,
	); err != nil {
		return nil, err
	}
	if limit.Valid {
		value := limit.Float64
		row.InferredLimitUSD = &value
	}
	if len(breakdown) > 0 && string(breakdown) != "null" {
		if err := json.Unmarshal(breakdown, &row.ModelBreakdown); err != nil {
			return nil, fmt.Errorf("decode model_breakdown: %w", err)
		}
	}
	if row.ModelBreakdown == nil {
		row.ModelBreakdown = []usagestats.AccountWindowModelStat{}
	}
	return &row, nil
}
