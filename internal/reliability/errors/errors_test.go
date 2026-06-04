package errors

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"
)

func TestFabricError_Properties(t *testing.T) {
	err := Wrap(ErrInsufficientRAM, "tried to load llama3-70b", errors.New("oom"))

	assert.Equal(t, "INSUFFICIENT_RAM", err.Code)
	assert.Contains(t, err.Error(), "tried to load llama3-70b")
	assert.Contains(t, err.Error(), "oom")
	assert.False(t, err.IsRetryable())
	assert.Equal(t, ErrInsufficientRAM.UserMessage, UserMessage(err))
}

func TestFabricError_Is(t *testing.T) {
	err1 := Wrap(ErrInsufficientRAM, "d1", nil)
	err2 := Wrap(ErrInsufficientRAM, "d2", nil)
	err3 := Wrap(ErrNodeUnavailable, "d3", nil)

	assert.True(t, err1.Is(err2))
	assert.False(t, err1.Is(err3))
}

func TestSafeGo_RecoversPanic(t *testing.T) {
	log := zaptest.NewLogger(t)
	beforeCount := PanicCount()

	ch := make(chan struct{})
	SafeGo(log, "test_panic_goroutine", func() {
		defer close(ch)
		panic("something went terribly wrong")
	})

	<-ch
	// Wait a moment for panic count to increment
	time.Sleep(10 * time.Millisecond)
	afterCount := PanicCount()

	assert.Equal(t, beforeCount+1, afterCount)
}

func TestSafeGoRestart_Restarts(t *testing.T) {
	log := zaptest.NewLogger(t)

	ch := make(chan int, 5)
	count := 0

	SafeGoRestart(log, "test_restart_goroutine", 2, func() {
		count++
		ch <- count
		if count < 3 {
			panic("simulated restart panic")
		}
	})

	// We expect count to reach 1, 2, then 3 (successful final run)
	select {
	case v := <-ch:
		assert.Equal(t, 1, v)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for run 1")
	}

	select {
	case v := <-ch:
		assert.Equal(t, 2, v)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for run 2")
	}

	select {
	case v := <-ch:
		assert.Equal(t, 3, v)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for run 3")
	}
}

func TestTransientByString(t *testing.T) {
	assert.True(t, isTransientByString(fmt.Errorf("connection refused")))
	assert.True(t, isTransientByString(fmt.Errorf("timeout occurred")))
	assert.False(t, isTransientByString(fmt.Errorf("syntax error")))
	assert.False(t, isTransientByString(nil))
}
