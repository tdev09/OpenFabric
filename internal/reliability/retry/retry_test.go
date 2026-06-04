package retry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

type mockPermanentError struct {
	msg string
}

func (e *mockPermanentError) Error() string {
	return e.msg
}

func (e *mockPermanentError) IsRetryable() bool {
	return false
}

func TestRetry_SucceedsOnSecondAttempt(t *testing.T) {
	log := zaptest.NewLogger(t)
	attempts := 0

	err := Do(context.Background(), PolicyNetwork, log, "test_op",
		func(attempt int) error {
			attempts++
			if attempt < 2 {
				return fmt.Errorf("connection refused")
			}
			return nil
		})

	assert.NoError(t, err)
	assert.Equal(t, 2, attempts)
}

func TestRetry_PermanentErrorNoRetry(t *testing.T) {
	log := zaptest.NewLogger(t)
	attempts := 0

	err := Do(context.Background(), PolicyNetwork, log, "test_op",
		func(attempt int) error {
			attempts++
			return &mockPermanentError{msg: "permanent error"}
		})

	assert.Error(t, err)
	assert.Equal(t, 1, attempts, "permanent error should not retry")
}

func TestRetry_ContextCancellation(t *testing.T) {
	log := zaptest.NewLogger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := Do(ctx, PolicyNetwork, log, "test_op",
		func(attempt int) error {
			return fmt.Errorf("connection refused")
		})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

func TestRetry_ExponentialBackoffGrowth(t *testing.T) {
	// Verify that delays grow exponentially
	delays := make([]time.Duration, 0)
	policy := Policy{
		MaxAttempts:    5,
		InitialDelay:   10 * time.Millisecond,
		MaxDelay:       1 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0, // no jitter for deterministic test
	}

	for attempt := 1; attempt <= 4; attempt++ {
		d := computeDelay(attempt, policy)
		delays = append(delays, d)
	}

	for i := 1; i < len(delays); i++ {
		assert.Greater(t, delays[i], delays[i-1],
			"delay should grow each attempt")
	}
}
