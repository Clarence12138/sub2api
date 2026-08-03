-- 中转入口标识：由边缘 Caddy 注入 X-Edge-Name / X-Edge-Host，直连为空。
-- Nullable、无 default：PG11+ 对大表为 metadata-only 变更，不重写 usage_logs。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS edge_name VARCHAR(32);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS entry_host VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_usage_logs_edge_name
  ON usage_logs (edge_name)
  WHERE edge_name IS NOT NULL AND edge_name <> '';
