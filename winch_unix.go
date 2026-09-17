//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize reports window resizes: SIGWINCH, which the tty sends whenever
// the terminal changes size. One buffered slot is a coalescing queue — dragging
// a window edge fires dozens of them, and what they all want is one repaint.
func watchResize() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}
