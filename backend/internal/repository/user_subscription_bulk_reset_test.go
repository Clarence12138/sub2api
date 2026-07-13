//go:build unit

package repository

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForEachIDBatchProcessesEveryIDWithoutTruncation(t *testing.T) {
	ids := make([]int64, subscriptionBulkResetBatchSize*2+17)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	var batchSizes []int
	var processed []int64
	err := forEachIDBatch(ids, func(batch []int64) error {
		batchSizes = append(batchSizes, len(batch))
		processed = append(processed, batch...)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, []int{subscriptionBulkResetBatchSize, subscriptionBulkResetBatchSize, 17}, batchSizes)
	require.Equal(t, ids, processed)
}

func TestForEachIDBatchStopsOnError(t *testing.T) {
	expected := errors.New("batch failed")
	calls := 0
	err := forEachIDBatch(make([]int64, subscriptionBulkResetBatchSize+1), func([]int64) error {
		calls++
		return expected
	})

	require.ErrorIs(t, err, expected)
	require.Equal(t, 1, calls)
}
