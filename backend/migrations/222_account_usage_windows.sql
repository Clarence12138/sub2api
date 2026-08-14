-- OpenAI Codex 5h/7d 官方窗口快照。开窗行持续记峰值占比，关窗时结算本站花费并反推额度。

CREATE TABLE IF NOT EXISTS account_usage_windows (
    id                   BIGSERIAL PRIMARY KEY,
    account_id           BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    window_type          VARCHAR(8) NOT NULL CHECK (window_type IN ('5h', '7d')),
    window_start         TIMESTAMPTZ NOT NULL,
    window_end           TIMESTAMPTZ NOT NULL,
    status               VARCHAR(8) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    closed_reason        VARCHAR(16) CHECK (closed_reason IS NULL OR closed_reason IN ('expired', 'reset_credit', 'probe')),
    peak_used_percent    DECIMAL(8,4) NOT NULL DEFAULT 0,
    last_used_percent    DECIMAL(8,4) NOT NULL DEFAULT 0,
    local_cost           DECIMAL(20,10) NOT NULL DEFAULT 0,
    standard_cost        DECIMAL(20,10) NOT NULL DEFAULT 0,
    user_cost            DECIMAL(20,10) NOT NULL DEFAULT 0,
    requests             BIGINT NOT NULL DEFAULT 0,
    tokens               BIGINT NOT NULL DEFAULT 0,
    inferred_limit_usd   DECIMAL(20,10),
    inferred_confidence  VARCHAR(8) NOT NULL DEFAULT 'low' CHECK (inferred_confidence IN ('high', 'medium', 'low')),
    model_breakdown      JSONB NOT NULL DEFAULT '[]'::jsonb,
    sampled_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (window_end > window_start)
);

CREATE UNIQUE INDEX IF NOT EXISTS account_usage_windows_account_type_end_uq
    ON account_usage_windows (account_id, window_type, window_end);

CREATE UNIQUE INDEX IF NOT EXISTS account_usage_windows_one_open_uq
    ON account_usage_windows (account_id, window_type)
    WHERE status = 'open';

CREATE INDEX IF NOT EXISTS account_usage_windows_account_type_end_idx
    ON account_usage_windows (account_id, window_type, window_end DESC);
