//go:build windows

package main

// reexec is nil on Windows, which has no execve: the upgrade notice stays up
// there, and the restart is whoever's at the keyboard to do.
var reexec func() error
