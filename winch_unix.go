//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// watchResize reports window resizes: SIGWINCH, which the tty sends whenever
// the terminal changes size. One buffered slot is a coalescing queue — dragging
// a window edge fires dozens of them, and what they all want is one repaint.
func watchResize() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

// cellHeight is how many pixels tall a row of the terminal is, or 0 when the
// terminal doesn't say.
func cellHeight() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 {
		return 0
	}
	return int(ws.Ypixel / ws.Row)
}
