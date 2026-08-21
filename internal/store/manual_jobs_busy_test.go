package store

import (
	"context"
	"errors"
	"testing"
)

func TestRetryManualJobPlayRetriesBusySnapshot(t *testing.T) {
	attempts := 0
	expected := ManualJobPlayResult{Idempotent: true}
	result, err := retryManualJobPlay(context.Background(), func() (ManualJobPlayResult, error) {
		attempts++
		if attempts < 3 {
			return ManualJobPlayResult{}, errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return expected, nil
	})
	if err != nil || attempts != 3 || result.Idempotent != expected.Idempotent {
		t.Fatalf("busy retry result = %#v, attempts=%d, err=%v", result, attempts, err)
	}
}

func TestRetryManualJobPlayHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	_, err := retryManualJobPlay(ctx, func() (ManualJobPlayResult, error) {
		attempts++
		return ManualJobPlayResult{}, errors.New("database is locked (5) (SQLITE_BUSY)")
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("cancelled retry attempts=%d, err=%v", attempts, err)
	}
}
