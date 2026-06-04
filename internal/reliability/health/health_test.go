package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHealthRegistry_RegisterAndRun(t *testing.T) {
	r := NewRegistry(100*time.Millisecond, 50*time.Millisecond)

	r.Register("test_check_1", func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:    "test_check_1",
			Status:  StatusHealthy,
			Message: "OK",
		}
	})

	r.Register("test_check_2", func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:    "test_check_2",
			Status:  StatusUnhealthy,
			Message: "Failed",
		}
	})

	results := r.RunAll(context.Background())
	assert.Len(t, results, 2)
	assert.Equal(t, StatusHealthy, results["test_check_1"].Status)
	assert.Equal(t, StatusUnhealthy, results["test_check_2"].Status)

	overall, aggregatedResults := r.Aggregate()
	assert.Equal(t, StatusUnhealthy, overall)
	assert.Equal(t, StatusUnhealthy, aggregatedResults["test_check_2"].Status)
}

func TestHealthRegistry_TimeoutAndDegraded(t *testing.T) {
	r := NewRegistry(100*time.Millisecond, 20*time.Millisecond)

	r.Register("slow_check", func(ctx context.Context) CheckResult {
		select {
		case <-ctx.Done():
			return CheckResult{
				Name:    "slow_check",
				Status:  StatusUnhealthy,
				Message: "timeout reached",
				Error:   ctx.Err().Error(),
			}
		case <-time.After(50 * time.Millisecond):
			return CheckResult{
				Name:    "slow_check",
				Status:  StatusHealthy,
				Message: "completed",
			}
		}
	})

	r.Register("degraded_check", func(ctx context.Context) CheckResult {
		return CheckResult{
			Name:    "degraded_check",
			Status:  StatusDegraded,
			Message: "high usage",
		}
	})

	results := r.RunAll(context.Background())
	assert.Equal(t, StatusUnhealthy, results["slow_check"].Status, "slow check should timeout and return unhealthy")
	assert.Equal(t, StatusDegraded, results["degraded_check"].Status)

	overall, _ := r.Aggregate()
	assert.Equal(t, StatusUnhealthy, overall)
}

func TestHealthRegistry_AggregateHealthyAndDegraded(t *testing.T) {
	r := NewRegistry(100*time.Millisecond, 50*time.Millisecond)

	r.Register("check_1", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "check_1", Status: StatusHealthy}
	})
	r.Register("check_2", func(ctx context.Context) CheckResult {
		return CheckResult{Name: "check_2", Status: StatusDegraded}
	})

	r.RunAll(context.Background())
	overall, _ := r.Aggregate()
	assert.Equal(t, StatusDegraded, overall)
}

func TestHealthRegistry_ChecksRunConcurrently(t *testing.T) {
	r := NewRegistry(100*time.Millisecond, 100*time.Millisecond)
	start := time.Now()

	r.Register("c1", func(ctx context.Context) CheckResult {
		time.Sleep(30 * time.Millisecond)
		return CheckResult{Name: "c1", Status: StatusHealthy}
	})
	r.Register("c2", func(ctx context.Context) CheckResult {
		time.Sleep(30 * time.Millisecond)
		return CheckResult{Name: "c2", Status: StatusHealthy}
	})

	r.RunAll(context.Background())
	duration := time.Since(start)

	// If checks ran sequentially, total duration would be >= 60ms.
	// Since they run concurrently, it should be significantly less than 60ms.
	assert.Less(t, duration, 55*time.Millisecond, "checks should run in parallel")
}

func TestHealthRegistry_StartStopLoop(t *testing.T) {
	r := NewRegistry(10*time.Millisecond, 5*time.Millisecond)
	count := 0
	r.Register("c1", func(ctx context.Context) CheckResult {
		count++
		return CheckResult{Name: "c1", Status: StatusHealthy}
	})

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)

	// Wait for background loop to run a few times
	time.Sleep(45 * time.Millisecond)
	cancel()

	lastCount := count
	assert.GreaterOrEqual(t, lastCount, 2, "should run at least twice in background loop")

	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, lastCount, count, "should stop executing checks after context cancellation")
}

func TestHealth_ChecksHelpers(t *testing.T) {
	res1 := unhealthy(CheckResult{Name: "test"}, "err")
	assert.Equal(t, StatusUnhealthy, res1.Status)
	assert.Equal(t, "err", res1.Message)

	res2 := degraded(CheckResult{Name: "test"}, "warn")
	assert.Equal(t, StatusDegraded, res2.Status)
	assert.Equal(t, "warn", res2.Message)
}

func TestHealth_CheckMCPServer(t *testing.T) {
	cSuccess := CheckMCPServer("m1", func(ctx context.Context) error {
		return nil
	})
	resSuccess := cSuccess(context.Background())
	assert.Equal(t, StatusHealthy, resSuccess.Status)

	cFail := CheckMCPServer("m1", func(ctx context.Context) error {
		return errors.New("not running")
	})
	resFail := cFail(context.Background())
	assert.Equal(t, StatusDegraded, resFail.Status)
	assert.Contains(t, resFail.Message, "not responding")
}

func TestHealth_CheckLibp2p(t *testing.T) {
	cZero := CheckLibp2p(func() int { return 0 })
	resZero := cZero(context.Background())
	assert.Equal(t, StatusDegraded, resZero.Status)
	assert.Contains(t, resZero.Message, "solo mode")

	cConnected := CheckLibp2p(func() int { return 3 })
	resConnected := cConnected(context.Background())
	assert.Equal(t, StatusHealthy, resConnected.Status)
	assert.Contains(t, resConnected.Message, "3 peer(s)")
}

func TestHealth_CheckGoroutineLeaks(t *testing.T) {
	// Baseline is very high, so it is healthy
	cHealthy := CheckGoroutineLeaks(10000, 2.0)
	resHealthy := cHealthy(context.Background())
	assert.Equal(t, StatusHealthy, resHealthy.Status)

	// Baseline is 1, current goroutines will be greater than 3, so it is degraded
	cDegraded := CheckGoroutineLeaks(1, 1.5)
	resDegraded := cDegraded(context.Background())
	assert.Equal(t, StatusDegraded, resDegraded.Status)
}
