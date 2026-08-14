package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestSameAccountWindowSkew(t *testing.T) {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	require.True(t, sameAccountWindow(base, base.Add(accountWindowRollSkew)))
	require.False(t, sameAccountWindow(base, base.Add(accountWindowRollSkew+time.Minute)))
}

func TestDefaultWindowClosedReason(t *testing.T) {
	end := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, usagestats.AccountWindowClosedResetCredit, defaultWindowClosedReason(usagestats.AccountWindowClosedResetCredit, end, end.Add(-time.Hour)))
	require.Equal(t, usagestats.AccountWindowClosedExpired, defaultWindowClosedReason("", end, end.Add(time.Minute)))
	require.Equal(t, usagestats.AccountWindowClosedProbe, defaultWindowClosedReason("", end, end.Add(-time.Minute)))
}
