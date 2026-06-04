package health

import (
	"context"
	"sync"
	"time"
)

// Status represents a health check result.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"  // functional but not optimal
	StatusUnhealthy Status = "unhealthy" // non-functional
	StatusUnknown   Status = "unknown"   // check hasn't run yet
)

// CheckResult holds the result of a single health check.
type CheckResult struct {
	Name      string        `json:"name"`
	Status    Status        `json:"status"`
	Message   string        `json:"message"`
	Duration  time.Duration `json:"duration_ms"`
	LastCheck time.Time     `json:"last_check"`
	Error     string        `json:"error,omitempty"`
}

// Checker is a function that performs a health check.
// Must return within the given context deadline.
type Checker func(ctx context.Context) CheckResult

// Registry manages all health checks.
type Registry struct {
	mu       sync.RWMutex
	checkers map[string]Checker
	results  map[string]CheckResult
	interval time.Duration // how often to re-run checks
	timeout  time.Duration // per-check timeout
}

// NewRegistry creates a health check Registry.
func NewRegistry(interval, timeout time.Duration) *Registry {
	return &Registry{
		checkers: make(map[string]Checker),
		results:  make(map[string]CheckResult),
		interval: interval,
		timeout:  timeout,
	}
}

// Register adds a named health check to the registry.
func (r *Registry) Register(name string, checker Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkers[name] = checker
	r.results[name] = CheckResult{
		Name:   name,
		Status: StatusUnknown,
	}
}

// RunAll executes all registered checks concurrently.
// Returns when all checks complete or their individual timeouts expire.
func (r *Registry) RunAll(ctx context.Context) map[string]CheckResult {
	r.mu.RLock()
	checkers := make(map[string]Checker, len(r.checkers))
	for name, checker := range r.checkers {
		checkers[name] = checker
	}
	r.mu.RUnlock()

	results := make(map[string]CheckResult, len(checkers))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, checker := range checkers {
		wg.Add(1)
		go func(name string, checker Checker) {
			defer wg.Done()

			// Each check has its own timeout
			checkCtx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()

			start := time.Now()
			result := checker(checkCtx)
			result.Duration = time.Since(start)
			result.LastCheck = time.Now()

			mu.Lock()
			results[name] = result
			mu.Unlock()
		}(name, checker)
	}

	wg.Wait()

	// Update stored results
	r.mu.Lock()
	for name, result := range results {
		r.results[name] = result
	}
	r.mu.Unlock()

	return results
}

// Aggregate returns the overall system health from all check results.
// Any unhealthy check makes the system unhealthy.
// Any degraded check (with all others healthy) makes it degraded.
func (r *Registry) Aggregate() (Status, map[string]CheckResult) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make(map[string]CheckResult, len(r.results))
	for k, v := range r.results {
		results[k] = v
	}

	overall := StatusHealthy
	for _, result := range results {
		switch result.Status {
		case StatusUnhealthy:
			return StatusUnhealthy, results // fail fast on unhealthy
		case StatusDegraded:
			overall = StatusDegraded // continue checking
		case StatusUnknown:
			if overall == StatusHealthy {
				overall = StatusUnknown
			}
		}
	}

	return overall, results
}

// Start begins the background health check loop.
func (r *Registry) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		// Run once immediately on start
		r.RunAll(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.RunAll(ctx)
			}
		}
	}()
}
