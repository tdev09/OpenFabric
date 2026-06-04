package retry

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Do executes fn with retry logic according to policy.
// fn receives the attempt number (1-indexed) for logging.
// Returns nil on success, or the last error after exhausting retries.
func Do(ctx context.Context, policy Policy, log *zap.Logger, opName string, fn func(attempt int) error) error {
	var lastErr error

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: context cancelled after %d attempts: %w",
				opName, attempt-1, ctx.Err())
		default:
		}

		start := time.Now()
		err := fn(attempt)
		elapsed := time.Since(start)

		if err == nil {
			if attempt > 1 {
				log.Info("operation succeeded after retry",
					zap.String("op", opName),
					zap.Int("attempt", attempt),
					zap.Duration("elapsed", elapsed),
				)
			}
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryable(err, policy.RetryableErrors) {
			log.Debug("operation failed with non-retryable error",
				zap.String("op", opName),
				zap.Error(err),
			)
			return fmt.Errorf("%s: permanent failure: %w", opName, err)
		}

		// Last attempt - don't sleep, just return the error
		if attempt == policy.MaxAttempts {
			break
		}

		delay := computeDelay(attempt, policy)

		log.Debug("operation failed, retrying",
			zap.String("op", opName),
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", policy.MaxAttempts),
			zap.Duration("retry_in", delay),
			zap.Error(err),
		)

		// Wait with context awareness
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: context cancelled during retry backoff: %w",
				opName, ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("%s: failed after %d attempts: %w",
		opName, policy.MaxAttempts, lastErr)
}

// computeDelay returns the backoff duration for a given attempt number.
// Applies exponential growth with jitter to prevent thundering herd.
func computeDelay(attempt int, policy Policy) time.Duration {
	// Exponential: initialDelay * multiplier^(attempt-1)
	base := float64(policy.InitialDelay) *
		math.Pow(policy.Multiplier, float64(attempt-1))

	// Cap at MaxDelay
	if base > float64(policy.MaxDelay) {
		base = float64(policy.MaxDelay)
	}

	// Add jitter: ±JitterFraction * base
	// Use a local random source to avoid global seeding issues
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(attempt)))
	jitter := (rng.Float64()*2 - 1) * policy.JitterFraction * base
	delay := time.Duration(base + jitter)

	// Ensure delay is never negative
	if delay < 0 {
		delay = time.Duration(base)
	}

	return delay
}

// isRetryable checks if an error should trigger a retry.
func isRetryable(err error, retryableClasses []ErrorClass) bool {
	if len(retryableClasses) == 0 {
		return true // no filter = retry all errors
	}

	class := ClassifyError(err)
	for _, c := range retryableClasses {
		if class == c {
			return true
		}
	}
	return false
}

// ClassifyError determines the ErrorClass of an error for retry decisions.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorPermanent
	}
	// Use duck typing to check for custom retryability interface and avoid cyclic import
	if r, ok := err.(interface{ IsRetryable() bool }); ok {
		if r.IsRetryable() {
			return ErrorTransient
		}
		return ErrorPermanent
	}
	if isTransientByString(err) {
		return ErrorTransient
	}
	return ErrorPermanent
}

func isTransientByString(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientPatterns := []string{
		"connection refused", "timeout", "i/o timeout",
		"no such host", "temporary failure", "eof",
		"broken pipe", "reset by peer", "context deadline exceeded",
	}
	lower := strings.ToLower(msg)
	for _, p := range transientPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// WithIdempotencyKey wraps fn to prevent duplicate side effects on retry.
// Uses a store to track which keys have already succeeded.
func WithIdempotencyKey(key string, store IdempotencyStore, fn func() error) func(attempt int) error {
	return func(attempt int) error {
		// Check if this key already succeeded
		if store.HasSucceeded(key) {
			return nil // already done - idempotent success
		}

		err := fn()
		if err == nil {
			store.MarkSucceeded(key)
		}
		return err
	}
}

// IdempotencyStore tracks which operations have already completed.
type IdempotencyStore interface {
	HasSucceeded(key string) bool
	MarkSucceeded(key string)
}

// MemoryIdempotencyStore is an in-memory IdempotencyStore.
// Use for operations within a single agent lifecycle.
type MemoryIdempotencyStore struct {
	mu   sync.RWMutex
	keys map[string]bool
}

// NewMemoryIdempotencyStore creates a MemoryIdempotencyStore.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{keys: make(map[string]bool)}
}

// HasSucceeded returns true if the key has been processed successfully.
func (s *MemoryIdempotencyStore) HasSucceeded(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keys[key]
}

// MarkSucceeded sets the key status to successfully processed.
func (s *MemoryIdempotencyStore) MarkSucceeded(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key] = true
}
