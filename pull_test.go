package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// `p` used to come back having done nothing at all, now and again: the
// background fetch was taking refs/remotes/origin/main out from under the
// pull's own fetch, which then refused to move a ref it no longer recognised
// and merged nothing. So run the two together, as hard as they will go, and
// insist the pull still lands.
func TestPullSurvivesTheBackgroundFetch(t *testing.T) {
	remote := initRepo(t) // the "remote": a repo with main one commit along
	clone := t.TempDir()
	git(t, "clone", "-q", remote, clone)
	git(t, "-C", clone, "config", "core.hooksPath", "/dev/null")

	// Three commits the clone hasn't got, so a pull that works is visible.
	for _, m := range []string{"two", "three", "four"} {
		git(t, "-C", remote, "commit", "--allow-empty", "-m", m)
	}

	ctx, stop := context.WithCancel(t.Context())
	go fetchMain(ctx, time.Nanosecond, []string{clone}) // as hard as it goes
	defer stop()

	if msg := gitPull(clone); strings.HasPrefix(msg, "pull failed") {
		t.Errorf("gitPull() = %q", msg)
	}
	out, err := exec.Command("git", "-C", clone, "log", "--oneline", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "four" {
		t.Errorf("after the pull the clone is at %q, want the remote's %q", got, "four")
	}
}

// The first `p` behind a fetched upstream is a fast-forward to what was
// fetched, with no remote asked: so take the remote away after the fetch and
// insist the first pull still lands, and that the second, with nothing fetched
// left to catch up on, goes to the remote — and finds it gone.
func TestPullFastForwardsToWhatWasFetched(t *testing.T) {
	remote := initRepo(t)
	clone := t.TempDir()
	git(t, "clone", "-q", remote, clone)
	git(t, "-C", remote, "commit", "--allow-empty", "-m", "two")
	git(t, "-C", clone, "fetch", "-q")
	git(t, "-C", clone, "remote", "set-url", "origin", t.TempDir()+"/gone")

	if msg := gitPull(clone); !strings.HasPrefix(msg, "pull: fast-forwarded 1 ") {
		t.Errorf("first gitPull() = %q, want a fast-forward", msg)
	}
	if msg := gitPull(clone); !strings.HasPrefix(msg, "pull failed") {
		t.Errorf("second gitPull() = %q, want it to have gone to the (gone) remote", msg)
	}
}
