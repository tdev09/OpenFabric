package errors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openfabric/openfabric/internal/reliability/retry"
)

// FabricError is the base error type for all OpenFabric errors.
// It carries a user-facing message separate from the technical detail.
type FabricError struct {
	// Code is a stable identifier for this error class.
	Code string
	// UserMessage is shown to the user in the dashboard. Plain English.
	UserMessage string
	// Detail is the technical error for logs. Never shown to users.
	Detail string
	// Wrapped is the underlying error cause.
	Wrapped error
	// Retryable indicates whether this error may resolve on retry.
	Retryable bool
}

// Error satisfies the Go error interface.
func (e *FabricError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s", e.Code, e.UserMessage))
	if e.Detail != "" {
		sb.WriteString(": ")
		sb.WriteString(e.Detail)
	}
	if e.Wrapped != nil {
		sb.WriteString(" (cause: ")
		sb.WriteString(e.Wrapped.Error())
		sb.WriteString(")")
	}
	return sb.String()
}

// Unwrap returns the wrapped cause.
func (e *FabricError) Unwrap() error { return e.Wrapped }

// Is implements error matching by Code.
func (e *FabricError) Is(target error) bool {
	t, ok := target.(*FabricError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// IsRetryable satisfies the retry package's duck-typed interface.
func (e *FabricError) IsRetryable() bool {
	return e.Retryable
}

// Predefined error codes - stable identifiers used in logs and metrics.
var (
	ErrNodeUnavailable = &FabricError{
		Code:        "NODE_UNAVAILABLE",
		UserMessage: "A cluster node became unavailable. Tasks are being rerouted.",
		Retryable:   true,
	}
	ErrOllamaNotRunning = &FabricError{
		Code:        "OLLAMA_NOT_RUNNING",
		UserMessage: "Ollama is not running on this device. Start it with: ollama serve",
		Retryable:   false,
	}
	ErrInsufficientRAM = &FabricError{
		Code:        "INSUFFICIENT_RAM",
		UserMessage: "Not enough RAM available. Add more devices to your cluster.",
		Retryable:   false,
	}
	ErrStorageWriteFailed = &FabricError{
		Code:        "STORAGE_WRITE_FAILED",
		UserMessage: "Could not save the file. Check that storage is not full.",
		Retryable:   true,
	}
	ErrTaskTimeout = &FabricError{
		Code:        "TASK_TIMEOUT",
		UserMessage: "The task timed out. It ran longer than the allowed time.",
		Retryable:   false,
	}
	ErrClusterNoLeader = &FabricError{
		Code:        "CLUSTER_NO_LEADER",
		UserMessage: "The cluster has no coordinator. It will self-elect shortly.",
		Retryable:   true,
	}
	ErrCommandNotAllowed = &FabricError{
		Code:        "COMMAND_NOT_ALLOWED",
		UserMessage: "This command is not on the allowed list. Add it in Settings → Task Security.",
		Retryable:   false,
	}
	ErrMCPServerDown = &FabricError{
		Code:        "MCP_SERVER_DOWN",
		UserMessage: "The integration is not responding. Check its configuration.",
		Retryable:   true,
	}
)

// Wrap creates a FabricError wrapping a standard error.
func Wrap(base *FabricError, detail string, cause error) *FabricError {
	return &FabricError{
		Code:        base.Code,
		UserMessage: base.UserMessage,
		Detail:      detail,
		Wrapped:     cause,
		Retryable:   base.Retryable,
	}
}

// IsRetryable returns true if the error may resolve on retry.
func IsRetryable(err error) bool {
	var fe *FabricError
	if errors.As(err, &fe) {
		return fe.Retryable
	}
	// Default: classify by error string patterns
	return isTransientByString(err)
}

// UserMessage extracts the user-facing message from any error.
// Falls back to a generic message for non-FabricErrors.
func UserMessage(err error) string {
	var fe *FabricError
	if errors.As(err, &fe) {
		return fe.UserMessage
	}
	return "Something went wrong. Check the logs for details."
}

// ClassifyError determines the ErrorClass of an error for retry decisions.
func ClassifyError(err error) retry.ErrorClass {
	var fe *FabricError
	if errors.As(err, &fe) {
		if fe.Retryable {
			return retry.ErrorTransient
		}
		return retry.ErrorPermanent
	}
	return retry.ErrorTransient // default: assume transient
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
