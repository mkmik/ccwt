//go:build windows

package main

import (
	"errors"
	"os"
)

// watchResize has nothing to watch on Windows, which has no SIGWINCH. A nil
// channel blocks forever, so there the frame goes on noticing a resize the way
// it always did: on the next interval tick.
func watchResize() (<-chan os.Signal, func()) { return nil, func() {} }

// watchSummon has nothing to watch either: there is no SIGUSR1 to summon the
// picker with.
func watchSummon() (<-chan os.Signal, func()) { return nil, func() {} }

func summon(int) error { return errors.New("ccwt open needs SIGUSR1, which Windows doesn't have") }

// cellHeight is 0, a terminal that doesn't say, on Windows.
func cellHeight() int { return 0 }
