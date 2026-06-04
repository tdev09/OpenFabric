package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

func TestComponent_NormalLifecycle(t *testing.T) {
	log := zaptest.NewLogger(t)

	started := false
	stopped := false

	comp := New("test-comp", log,
		func(ctx context.Context) error {
			started = true
			return nil
		},
		func(ctx context.Context) error {
			stopped = true
			return nil
		},
	)

	assert.Equal(t, StateNew, comp.State())

	err := comp.Start(context.Background())
	assert.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, StateRunning, comp.State())
	assert.True(t, comp.IsRunning())

	err = comp.Shutdown(1 * time.Second)
	assert.NoError(t, err)
	assert.True(t, stopped)
	assert.Equal(t, StateStopped, comp.State())
	assert.False(t, comp.IsRunning())
}

func TestComponent_StartError(t *testing.T) {
	log := zaptest.NewLogger(t)

	comp := New("test-comp-err", log,
		func(ctx context.Context) error {
			return errors.New("failed dependency")
		},
		func(ctx context.Context) error {
			return nil
		},
	)

	err := comp.Start(context.Background())
	assert.Error(t, err)
	assert.Equal(t, StateError, comp.State())
}

func TestComponent_IllegalTransitions(t *testing.T) {
	log := zaptest.NewLogger(t)

	comp := New("test-comp-illegal", log,
		func(ctx context.Context) error { return nil },
		func(ctx context.Context) error { return nil },
	)

	// Shutdown before Start
	err := comp.Shutdown(1 * time.Second)
	assert.Error(t, err)

	// Double start
	err = comp.Start(context.Background())
	assert.NoError(t, err)

	err = comp.Start(context.Background())
	assert.Error(t, err)
}
