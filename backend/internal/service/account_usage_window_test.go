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

func TestBuildOpenWindowUsesPreviousReset(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	prev := now.Add(-3 * 24 * time.Hour)
	reset := now.Add(4 * 24 * time.Hour)
	row := buildOpenWindow(1, usagestats.AccountWindowType7d, 12, &reset, now, &prev)
	require.NotNil(t, row)
	require.Equal(t, prev, row.WindowStart)
	require.Equal(t, reset, row.WindowEnd)
}

func TestShouldKeepTrajectorySample(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	prev := &usagestats.AccountWindowSample{SampledAt: base, UsedPercent: 10, StandardCost: 5}
	require.False(t, shouldKeepTrajectorySample(prev, usagestats.AccountWindowSample{
		SampledAt: base.Add(time.Minute), UsedPercent: 10.1, StandardCost: 5.1,
	}))
	require.True(t, shouldKeepTrajectorySample(prev, usagestats.AccountWindowSample{
		SampledAt: base.Add(time.Minute), UsedPercent: 11, StandardCost: 5.1,
	}))
	require.True(t, shouldKeepTrajectorySample(nil, usagestats.AccountWindowSample{UsedPercent: 1}))
}

func TestDefaultWindowClosedReason(t *testing.T) {
	end := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, usagestats.AccountWindowClosedResetCredit, defaultWindowClosedReason(usagestats.AccountWindowClosedResetCredit, end, end.Add(-time.Hour)))
	require.Equal(t, usagestats.AccountWindowClosedExpired, defaultWindowClosedReason("", end, end.Add(time.Minute)))
	require.Equal(t, usagestats.AccountWindowClosedProbe, defaultWindowClosedReason("", end, end.Add(-time.Minute)))
}
