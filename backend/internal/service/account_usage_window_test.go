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

func TestShouldStayOnOpenWindow7dIgnoresResetJitter(t *testing.T) {
	end := time.Date(2026, 8, 20, 3, 33, 43, 0, time.UTC)
	open := &usagestats.AccountUsageWindow{
		WindowType:      usagestats.AccountWindowType7d,
		WindowEnd:       end,
		PeakUsedPercent: 23,
		LastUsedPercent: 23,
	}

	jitter := end.Add(3 * time.Minute)
	require.True(t, shouldStayOnOpenWindow(open, 23, &jitter))
	back := end.Add(-3 * time.Minute)
	require.True(t, shouldStayOnOpenWindow(open, 23, &back))
	require.True(t, shouldStayOnOpenWindow(open, 23, nil))

	// 超过 30 分钟但仍不到数小时，占比没掉：继续挂原窗。
	drift := end.Add(2 * time.Hour)
	require.True(t, shouldStayOnOpenWindow(open, 23, &drift))
	require.True(t, shouldStayOnOpenWindow(open, 22, &drift))

	// 占比明显下降，即使 reset_at 只挪了不到 6 小时，也换窗。
	dropped := end.Add(40 * time.Minute)
	require.False(t, shouldStayOnOpenWindow(open, 1, &dropped))

	// 漏掉了占比回落，但 reset_at 跳了大半天：换窗。
	jumped := end.Add(7 * 24 * time.Hour)
	require.False(t, shouldStayOnOpenWindow(open, 22, &jumped))
}

func TestShouldStayOnOpenWindow5hKeepsTightSkew(t *testing.T) {
	end := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	open := &usagestats.AccountUsageWindow{
		WindowType:      usagestats.AccountWindowType5h,
		WindowEnd:       end,
		PeakUsedPercent: 40,
		LastUsedPercent: 40,
	}
	near := end.Add(accountWindowRollSkew)
	require.True(t, shouldStayOnOpenWindow(open, 40, &near))
	far := end.Add(accountWindowRollSkew + time.Minute)
	require.False(t, shouldStayOnOpenWindow(open, 40, &far))
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
