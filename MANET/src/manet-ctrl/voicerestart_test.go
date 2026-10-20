package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// A burst of unauthenticated TX channel changes must cost one restart.
func TestRequestVoiceRestartCoalesces(t *testing.T) {
	var calls atomic.Int32
	oldDelay, oldCmd := voiceRestartDelay, voiceRestartCmd
	voiceRestartDelay = 50 * time.Millisecond
	voiceRestartCmd = func() ([]byte, error) { calls.Add(1); return nil, nil }
	t.Cleanup(func() { voiceRestartDelay, voiceRestartCmd = oldDelay, oldCmd })

	for i := 0; i < 20; i++ {
		requestVoiceRestart()
	}
	time.Sleep(150 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("20 requests caused %d restarts, want 1", n)
	}

	// A change after the restart fired gets its own restart.
	requestVoiceRestart()
	time.Sleep(150 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("later request: %d restarts total, want 2", n)
	}
}
