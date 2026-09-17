//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// reexec replaces this process with whatever binary is at its own path now:
// execve, with the same arguments and environment, so the pid, the pane and
// the shell's job are all still this one, and the tui comes back with the
// flags it was started with. It only returns when that failed, and then
// nothing has changed. nil where the platform has no execve (see
// restart_windows.go), which is how the tui knows not to ask.
//
// execve rather than a child to wait on: that would leave a stack of old ccwts
// behind a tui upgraded under twice, and hand herdr a pane whose pid isn't the
// tui's any more.
var reexec = func() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	return fmt.Errorf("restart into %s: %w", exe, syscall.Exec(exe, os.Args, os.Environ()))
}
