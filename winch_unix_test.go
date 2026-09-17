//go:build !windows

package main

import (
	"syscall"
	"testing"
	"time"
)

// A resize has to reach the loop, or the frame sits at the old size until the
// next tick — which is the whole point of watching for it.
func TestWatchResize(t *testing.T) {
	ch, stop := watchResize()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no resize reported")
	}
}
