//go:build windows

package main

import "os"

// watchResize has nothing to watch on Windows, which has no SIGWINCH. A nil
// channel blocks forever, so there the frame goes on noticing a resize the way
// it always did: on the next interval tick.
func watchResize() (<-chan os.Signal, func()) { return nil, func() {} }

// cellHeight is 0, a terminal that doesn't say, on Windows.
func cellHeight() int { return 0 }
