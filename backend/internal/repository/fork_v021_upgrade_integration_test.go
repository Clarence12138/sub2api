//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Exercise the deployed fork schema followed by the release schema in a separate
// disposable database, never in the shared fixture database or a production DSN.
func TestForkV021Upgrade_PreservesDataAndLegacyMigrationCompatibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("fork_upgrade"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	legacy := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= "232" {
			continue
		}
		data, readErr := fs.ReadFile(migrations.FS, name)
		require.NoError(t, readErr)
		legacy[name] = &fstest.MapFile{Data: data}
	}
	for _, name := range []string{"192_add_usage_log_edge_ingress.sql", "222_account_usage_windows.sql", "223_account_usage_window_samples.sql"} {
		require.Contains(t, legacy, name)
	}
	require.NoError(t, applyMigrationsFS(ctx, db, legacy))
	var userID, accountID, keyID, windowID int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ('upgrade@example.invalid','test-only') RETURNING id`).Scan(&userID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO accounts (name,platform,type) VALUES ('upgrade','openai','oauth') RETURNING id`).Scan(&accountID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO api_keys (user_id,key,name) VALUES ($1,'test-only-upgrade','upgrade') RETURNING id`, userID).Scan(&keyID))
	legacyInsert := `INSERT INTO usage_logs (user_id,api_key_id,account_id,request_id,model,edge_name,entry_host) VALUES ($1,$2,$3,$4,'gpt-5.5','test-edge','test.invalid')`
	_, err = db.ExecContext(ctx, legacyInsert, userID, keyID, accountID, "before-upgrade")
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO account_usage_windows (account_id,window_type,window_start,window_end,standard_cost) VALUES ($1,'7d',now(),now()+interval '7 days',12.5) RETURNING id`, accountID).Scan(&windowID))
	_, err = db.ExecContext(ctx, `INSERT INTO account_usage_window_samples (window_id,sampled_at,used_percent,standard_cost,local_cost) VALUES ($1,now(),10,12.5,6.25)`, windowID)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db), "new version restart must be idempotent")
	require.NoError(t, applyMigrationsFS(ctx, db, legacy), "legacy runner must tolerate new migration records without checksum changes")
	_, err = db.ExecContext(ctx, legacyInsert, userID, keyID, accountID, "after-upgrade")
	require.NoError(t, err, "old write shape must remain valid")
	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE edge_name='test-edge' AND entry_host='test.invalid' AND upstream_request_id IS NULL`).Scan(&rows))
	require.Equal(t, 2, rows)
	var cost float64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT standard_cost FROM account_usage_windows WHERE id=$1`, windowID).Scan(&cost))
	require.Equal(t, 12.5, cost)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT standard_cost FROM account_usage_window_samples WHERE window_id=$1`, windowID).Scan(&cost))
	require.Equal(t, 12.5, cost)
	var forceFast, freeFast bool
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO groups (name) VALUES ('upgrade-defaults') RETURNING force_openai_fast,free_openai_fast`).Scan(&forceFast, &freeFast))
	require.False(t, forceFast)
	require.False(t, freeFast)
	var valid bool
	var definition string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT indisvalid,pg_get_indexdef(indexrelid) FROM pg_index WHERE indexrelid='idx_usage_logs_upstream_request_id'::regclass`).Scan(&valid, &definition))
	require.True(t, valid)
	require.True(t, strings.Contains(definition, "WHERE (upstream_request_id IS NOT NULL)"))
}
