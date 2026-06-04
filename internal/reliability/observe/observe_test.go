package observe

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestObserve_LoggerInitialization(t *testing.T) {
	log, err := NewLogger(true, "debug")
	assert.NoError(t, err)
	assert.NotNil(t, log)

	logProd, err := NewLogger(false, "info")
	assert.NoError(t, err)
	assert.NotNil(t, logProd)
}

func TestObserve_LoggerHelpers(t *testing.T) {
	log, _ := NewLogger(true, "info")

	compLog := WithComponent(log, "test-comp")
	assert.NotNil(t, compLog)

	taskLog := WithTask(log, "t-1", "n-1")
	assert.NotNil(t, taskLog)

	nodeLog := WithNode(log, "n-1")
	assert.NotNil(t, nodeLog)
}

func TestObserve_MetricsAndUptime(t *testing.T) {
	beforeValue := Metrics.UptimeSeconds.Value()

	ctx, cancel := context.WithCancel(context.Background())
	StartUptimeTracker(ctx)

	// Wait for uptime tracker to tick
	time.Sleep(1500 * time.Millisecond)
	cancel()

	afterValue := Metrics.UptimeSeconds.Value()
	assert.Greater(t, afterValue, beforeValue, "uptime seconds should increment")

	// Verify some standard metrics are registered
	Metrics.TasksSubmitted.Add(1)
	assert.Equal(t, int64(1), Metrics.TasksSubmitted.Value())
}
