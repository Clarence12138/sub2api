-- 窗内金额×占比轨迹。用于当前/历史窗口的斜率图。

CREATE TABLE IF NOT EXISTS account_usage_window_samples (
    id              BIGSERIAL PRIMARY KEY,
    window_id       BIGINT NOT NULL REFERENCES account_usage_windows(id) ON DELETE CASCADE,
    sampled_at      TIMESTAMPTZ NOT NULL,
    used_percent    DECIMAL(8,4) NOT NULL DEFAULT 0,
    standard_cost   DECIMAL(20,10) NOT NULL DEFAULT 0,
    local_cost      DECIMAL(20,10) NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS account_usage_window_samples_window_time_idx
    ON account_usage_window_samples (window_id, sampled_at ASC);
