package errors

import (
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/openfabric/openfabric/internal/reliability/observe"
	"go.uber.org/zap"
)

// panicCounter tracks total panics for the observability pipeline.
var panicCounter atomic.Int64

// PanicCount returns the total number of recovered panics since startup.
func PanicCount() int64 { return panicCounter.Load() }

// SafeGo launches a goroutine wrapped in panic recovery.
// If the goroutine panics, the panic is caught, logged with a stack trace,
// and optionally reported to the health system.
// The goroutine is NOT restarted - caller is responsible for that.
func SafeGo(log *zap.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				log.Error("goroutine panicked - recovered",
					zap.String("goroutine", name),
					zap.Any("panic", r),
					zap.ByteString("stack", stack),
				)
				// Emit a panic metric for observability
				panicCounter.Add(1)
				observe.Metrics.PanicCount.Add(1)
			}
		}()
		fn()
	}()
}

// SafeGoRestart is like SafeGo but automatically restarts the goroutine
// after a panic. Used for critical background goroutines that must
// always be running (e.g. heartbeat, health monitor).
// Restarts up to maxRestarts times before giving up.
func SafeGoRestart(log *zap.Logger, name string, maxRestarts int, fn func()) {
	go func() {
		restarts := 0
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						stack := debug.Stack()
						restarts++
						log.Error("critical goroutine panicked - restarting",
							zap.String("goroutine", name),
							zap.Any("panic", r),
							zap.Int("restart_count", restarts),
							zap.Int("max_restarts", maxRestarts),
							zap.ByteString("stack", stack),
						)
						panicCounter.Add(1)
						observe.Metrics.PanicCount.Add(1)
					}
				}()
				fn()
			}()

			if restarts > maxRestarts {
				log.Error("critical goroutine exceeded max restarts - giving up",
					zap.String("goroutine", name),
					zap.Int("restarts", restarts),
				)
				return
			}

			// Brief pause before restart to prevent tight panic loops
			time.Sleep(time.Duration(restarts) * time.Second)
		}
	}()
}
