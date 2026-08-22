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

func TestClassifyAccountWindowSample7d(t *testing.T) {
	end := time.Date(2026, 8, 27, 3, 33, 43, 0, time.UTC)
	open := &usagestats.AccountUsageWindow{
		WindowType:      usagestats.AccountWindowType7d,
		WindowEnd:       end,
		PeakUsedPercent: 23,
		LastUsedPercent: 23,
	}

	jitter := end.Add(3 * time.Minute)
	require.Equal(t, accountWindowSampleUpdate, classifyAccountWindowSample(open, &jitter, end.Add(-24*time.Hour)))
	back := end.Add(-3 * time.Minute)
	require.Equal(t, accountWindowSampleUpdate, classifyAccountWindowSample(open, &back, end.Add(-24*time.Hour)))
	require.Equal(t, accountWindowSampleUpdate, classifyAccountWindowSample(open, nil, end.Add(-24*time.Hour)))

	// A contradictory reset cannot roll an official window several days early.
	earlyNow := time.Date(2026, 8, 22, 3, 33, 43, 0, time.UTC)
	earlyReset := earlyNow.Add(7 * 24 * time.Hour)
	require.Equal(t, accountWindowSampleIgnore, classifyAccountWindowSample(open, &earlyReset, earlyNow))

	// Once the current window has ended, a reset for the following week rolls it.
	rollNow := end.Add(time.Second)
	nextEnd := end.Add(7 * 24 * time.Hour)
	require.Equal(t, accountWindowSampleRoll, classifyAccountWindowSample(open, &nextEnd, rollNow))

	stale := end.Add(-time.Hour)
	require.Equal(t, accountWindowSampleIgnore, classifyAccountWindowSample(open, &stale, rollNow))
}

func TestClassifyAccountWindowSample5hKeepsTightSkew(t *testing.T) {
	end := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	open := &usagestats.AccountUsageWindow{
		WindowType:      usagestats.AccountWindowType5h,
		WindowEnd:       end,
		PeakUsedPercent: 40,
		LastUsedPercent: 40,
	}
	near := end.Add(accountWindowRollSkew)
	require.Equal(t, accountWindowSampleUpdate, classifyAccountWindowSample(open, &near, end.Add(-time.Hour)))
	far := end.Add(accountWindowRollSkew + time.Minute)
	require.Equal(t, accountWindowSampleRoll, classifyAccountWindowSample(open, &far, end.Add(-time.Hour)))
}

func TestValidAccountWindowSample(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	used := 6.0
	reset := now.Add(7 * 24 * time.Hour)
	minutes := 7 * 24 * 60
	require.True(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, &reset, &minutes, now, true))

	zeroMinutes := 0
	require.False(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, &reset, &zeroMinutes, now, true))
	past := now
	require.False(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, &past, &minutes, now, true))
	invalidPercent := 101.0
	require.False(t, validAccountWindowSample(usagestats.AccountWindowType7d, &invalidPercent, &reset, &minutes, now, true))
	require.False(t, validAccountWindowSample(usagestats.AccountWindowType7d, nil, &reset, &minutes, now, true))
	require.False(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, nil, &minutes, now, true))
	require.True(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, nil, &minutes, now, false))

	eightDayMinutes := 8 * 24 * 60
	eightDayReset := now.Add(8 * 24 * time.Hour)
	require.True(t, validAccountWindowSample(usagestats.AccountWindowType7d, &used, &eightDayReset, &eightDayMinutes, now, true))
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
