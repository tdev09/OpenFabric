package retry

import "time"

// ErrorClass categorizes errors for retry decisions.
type ErrorClass string

const (
	// ErrorTransient is a temporary failure (network timeout, unavailable).
	ErrorTransient ErrorClass = "transient"
	// ErrorPermanent is a non-recoverable failure (invalid input, auth fail).
	ErrorPermanent ErrorClass = "permanent"
	// ErrorResourceExhausted is a rate limit or capacity error.
	ErrorResourceExhausted ErrorClass = "resource_exhausted"
)

// Policy defines retry behaviour for a class of operation.
type Policy struct {
	// MaxAttempts is the total number of attempts (1 = no retry).
	MaxAttempts int
	// InitialDelay is the wait before the first retry.
	InitialDelay time.Duration
	// MaxDelay caps the exponential backoff.
	MaxDelay time.Duration
	// Multiplier is the backoff growth factor (2.0 = double each time).
	Multiplier float64
	// JitterFraction adds randomness to prevent thundering herd.
	// 0.1 = ±10% of the current delay.
	JitterFraction float64
	// RetryableErrors defines which error types to retry.
	// If nil, all errors are retried.
	RetryableErrors []ErrorClass
}

// Pre-defined policies for common OpenFabric operations.
var (
	// PolicyNetwork is for libp2p stream operations and Ollama API calls.
	PolicyNetwork = Policy{
		MaxAttempts:    5,
		InitialDelay:   200 * time.Millisecond,
		MaxDelay:       30 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0.2,
		RetryableErrors: []ErrorClass{ErrorTransient, ErrorResourceExhausted},
	}

	// PolicyStorage is for file system operations.
	PolicyStorage = Policy{
		MaxAttempts:    3,
		InitialDelay:   100 * time.Millisecond,
		MaxDelay:       5 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0.1,
		RetryableErrors: []ErrorClass{ErrorTransient},
	}

	// PolicyMCP is for MCP server subprocess calls.
	PolicyMCP = Policy{
		MaxAttempts:    4,
		InitialDelay:   500 * time.Millisecond,
		MaxDelay:       15 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0.25,
		RetryableErrors: []ErrorClass{ErrorTransient, ErrorResourceExhausted},
	}

	// PolicyFast is for quick operations that should fail fast.
	PolicyFast = Policy{
		MaxAttempts:    2,
		InitialDelay:   50 * time.Millisecond,
		MaxDelay:       500 * time.Millisecond,
		Multiplier:     2.0,
		JitterFraction: 0.1,
	}

	// PolicyNone disables retries entirely.
	PolicyNone = Policy{MaxAttempts: 1}
)
