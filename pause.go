package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// `ccwt pause` quiets what every ccwt asks herdr on its own — the list's round
// and the ws view's every couple of seconds, the badge every minute, the
// corner's watch on the `ws` tabs — until `ccwt unpause`: a way to see whether
// herdr is slow because of them. What a key or a command does still reaches
// herdr: workspaces and tabs are made, opened, focused and closed as ever, and
// a removal still asks herdr whether an agent is working.
//
// The pause is a file in the state directory rather than a message to each
// ccwt, so it reaches every one of them, those not started yet included, and
// outlives them all.

type PauseCmd struct{}

func (c *PauseCmd) Run() error {
	path := pausePath()
	if path == "" {
		return errors.New("cannot locate a state directory: set XDG_STATE_HOME")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

type UnpauseCmd struct{}

func (c *UnpauseCmd) Run() error {
	if err := os.Remove(pausePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// pausePath is the file that says ccwt is paused, "" when there is no state
// directory to find it in.
func pausePath() string {
	if dir := stateDir(); dir != "" {
		return filepath.Join(dir, "paused")
	}
	return ""
}

// herdrPaused reports whether `ccwt pause` is in force. A stat, made each time
// the question comes up rather than kept up to date by a watcher: it costs
// microseconds against the round trip to herdr it stands in front of, and it
// comes up a few times every couple of seconds.
func herdrPaused() bool {
	_, err := os.Stat(pausePath())
	return err == nil
}

// pauseNote is what the bar says while ccwt is paused: the dots and names
// standing still would otherwise read as herdr having nothing new to say.
func pauseNote() string {
	if herdrPaused() {
		return errHerdrPaused.Error()
	}
	return ""
}

var errHerdrPaused = errors.New("herdr paused (ccwt unpause)")

// herdrAsker is how a question is put to herdr: herdrAsk or herdrPoll, for a
// question an action and a view both ask, each in its own way.
type herdrAsker func(method string, params map[string]any, args ...string) ([]byte, error)

// herdrPoll is herdrAsk for what ccwt asks and tells herdr on its own, to keep
// a view up to date, rather than because a key was pressed or a command run.
// Paused, herdr hears none of it: the answer is the last one herdr gave the same
// question, so a view stands still rather than going blank, and errHerdrPaused
// for a question it was never asked.
func herdrPoll(method string, params map[string]any, args ...string) ([]byte, error) {
	key := fmt.Sprintf("%q", args) // the cli's words for the request say all the socket's do
	if herdrPaused() {
		if out, ok := herdrHeard.Load(key); ok {
			return out.([]byte), nil
		}
		return nil, errHerdrPaused
	}
	out, err := herdrAsk(method, params, args...)
	if err == nil {
		herdrHeard.Store(key, out)
	}
	return out, err
}

// herdrHeard is herdrPoll's last answer to each question it has put.
var herdrHeard sync.Map
