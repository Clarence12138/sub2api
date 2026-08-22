package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func newAccountUsageWindowRepoMock(t *testing.T) (*accountUsageWindowRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &accountUsageWindowRepository{sql: db, db: db}, mock
}

func accountUsageWindowTestRows() (*usagestats.AccountUsageWindow, *usagestats.AccountUsageWindow) {
	now := time.Date(2026, 8, 27, 11, 33, 43, 0, time.UTC)
	closed := &usagestats.AccountUsageWindow{
		ID:                 75,
		AccountID:          20,
		WindowType:         usagestats.AccountWindowType7d,
		WindowStart:        now.Add(-7 * 24 * time.Hour),
		WindowEnd:          now,
		Status:             usagestats.AccountWindowStatusOpen,
		PeakUsedPercent:    6,
		LastUsedPercent:    6,
		InferredConfidence: usagestats.AccountWindowConfidenceLow,
		ModelBreakdown:     []usagestats.AccountWindowModelStat{},
		SampledAt:          now,
	}
	next := &usagestats.AccountUsageWindow{
		AccountID:          20,
		WindowType:         usagestats.AccountWindowType7d,
		WindowStart:        now,
		WindowEnd:          now.Add(7 * 24 * time.Hour),
		Status:             usagestats.AccountWindowStatusOpen,
		InferredConfidence: usagestats.AccountWindowConfidenceLow,
		ModelBreakdown:     []usagestats.AccountWindowModelStat{},
		SampledAt:          now,
	}
	return closed, next
}

func TestAccountUsageWindowUpdateSampleRejectsStaleWindow(t *testing.T) {
	repo, mock := newAccountUsageWindowRepoMock(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec(`(?s)UPDATE account_usage_windows.*WHERE id = \$1 AND status = 'open'`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.UpdateSample(context.Background(), 75, 6, 6, now)
	require.ErrorIs(t, err, errAccountUsageWindowConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountUsageWindowCloseAndOpenCommitsCompleteRoll(t *testing.T) {
	repo, mock := newAccountUsageWindowRepoMock(t)
	closed, next := accountUsageWindowTestRows()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE account_usage_windows.*WHERE id = \$1 AND status = 'open'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)INSERT INTO account_usage_windows.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(76)))
	mock.ExpectCommit()

	require.NoError(t, repo.CloseAndOpen(context.Background(), closed, next))
	require.Equal(t, int64(76), next.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountUsageWindowCloseAndOpenRollsBackStaleClose(t *testing.T) {
	repo, mock := newAccountUsageWindowRepoMock(t)
	closed, next := accountUsageWindowTestRows()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE account_usage_windows.*WHERE id = \$1 AND status = 'open'`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := repo.CloseAndOpen(context.Background(), closed, next)
	require.ErrorIs(t, err, errAccountUsageWindowConflict)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountUsageWindowCloseAndOpenRollsBackClosedTargetConflict(t *testing.T) {
	repo, mock := newAccountUsageWindowRepoMock(t)
	closed, next := accountUsageWindowTestRows()

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE account_usage_windows.*WHERE id = \$1 AND status = 'open'`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)INSERT INTO account_usage_windows.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	err := repo.CloseAndOpen(context.Background(), closed, next)
	require.True(t, errors.Is(err, errAccountUsageWindowConflict))
	require.Zero(t, next.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountUsageWindowUpsertOpenRejectsClosedTargetConflict(t *testing.T) {
	repo, mock := newAccountUsageWindowRepoMock(t)
	_, next := accountUsageWindowTestRows()

	mock.ExpectQuery(`(?s)INSERT INTO account_usage_windows.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	err := repo.UpsertOpen(context.Background(), next)
	require.ErrorIs(t, err, errAccountUsageWindowConflict)
	require.Zero(t, next.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
