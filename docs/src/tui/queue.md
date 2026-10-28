# Queued prompts

Work you've thought of but can't start yet — because it needs the worktree you're in to be
finished first — goes on the list as a prompt queued behind it. `n`, for what to do next,
opens a box over the middle of the list, and what you type there is recorded under the
selected row:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
          ┌─ dreamy-foraging-hickey ──────────────────────────┐
          │ rebase onto main, open the PR, and then port the  │
          │ widget to the mobile layout█                      │
          │                                                   │
          └───────────────────────────────────────────────────┘

 ↵:queue  ctrl-g:$EDITOR  esc:cancel
```

It's three fifths of the terminal, that size whatever is in it — a box that grew as you
typed would shift the list behind it line by line — and the row the prompt will hang off is
one of the ones still showing around it. `↵` records it, `esc` throws it away. Behind
another queued prompt the box's top rule names that prompt instead, since a chain can have
several links and the worktree's name wouldn't say which one you're extending.

Above it is a one-line title box, as on [`ccwt ws`](../herdr/ws.md#seeding-agents)'s seed
prompt. The box opens there: `↵` or `tab` takes you down to the prompt, and `shift-tab` takes
you back up. Leaving it empty is fine. A queued prompt with a title shows the title in TOPIC,
and the details pane and `e` still have the prompt in full. When the prompt starts, its title
becomes the agent's `--name` and the workspace's label.

## Chains

Once recorded it's a row of the list:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
* * dreamy-foraging-hickey  worktree-dreamy-…-hickey  2h   yes    ✳ Goal was a widget on…
    ↳ <queued>                                        4m          rebase onto main and open the PR
      ↳ <queued>                                      3m          then port the widget to the mobile layout
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
```

It has no worktree of its own yet, and `<queued>` in NAME says that's why the column is
otherwise empty, the way `<new>` does [below](#when-its-turn-comes) for a prompt whose turn
has come.

`n` on a queued prompt queues another one behind *that* one instead, so a chain of "and
then" is a tree hanging off the worktree it starts from. `d` shows a long prompt in full in
the details pane, and `r` deletes one — along with everything queued behind it, since
nothing waiting on a prompt that isn't going to happen can happen either.

## Moving a prompt

A prompt queued behind the wrong row — `n` pressed with the last prompt still selected, when
the new one didn't need to wait for it — is moved rather than typed again. "reorder" in the
[`☰` menu](index.md#the--menu), on a queued prompt, keeps the list up and gives the vim keys
to the prompt:

- `k` and `j` move it up and down among the prompts waiting on the same thing as it
- `h` takes it out from under the prompt it waits on: it waits on what that one waits on
  instead, and goes right after it
- `l` puts it under the prompt just above it, after whatever already waits there

What is queued behind it moves with it. The arrows still move the selection, so one visit to
the menu can move several prompts, and `esc` (or `↵`) is done. The menu is the only way in:
it's rare, and a key of its own would be one more to hit by accident.

## Editing a prompt

`e` in that details pane — where the prompt is legible in full, which is where you notice it
needs a word changing — reopens it in the same box for rewriting. The edit box takes the
pane's place rather than stacking on it, `↵` saves, and the prompt stays where it is in the
chain: whatever was waiting on it still is.

The box is a line editor, since the word that needs changing is rarely the last one, and its
keys are the ones the zsh line editor and Claude Code's own box use: `←` and `→` move the
caret, Ctrl-`←`/`→` (or Alt-B/F) move it a word at a time, `↑` and `↓` (or Ctrl-P/N) move it
a line at a time, `home` and `end` (or Ctrl-A and Ctrl-E) go to either end of the line,
`backspace` and `delete` take the character on their own side of it, and Ctrl-W, Alt-D,
Ctrl-U and Ctrl-K rub out the word before the caret, the word after it, the rest of the line
before it and the rest of the line after it. Text goes in where the caret is, and the box
scrolls to wherever that is, so the start of a prompt taller than the box is reachable.

Shift-`↵` puts a line break in — `↵` on its own records the prompt — so a prompt can be a
paragraph rather than a sentence. Only `↵` and `esc` close the box: a key it doesn't know,
whatever your terminal sends for Cmd-`←`, does nothing at all rather than throwing away what
you have typed. (A terminal has to be told to send something for shift-`↵`, since by default
it sends the same thing `↵` does. Claude Code's own `/terminal-setup` does that for iTerm2
and VS Code, and this box takes what it binds; Ctrl-J is the line break everywhere else.)
The table folds the breaks back to spaces for the one row it has to draw the prompt on.

For a prompt that wants more than that, Ctrl-G finishes it in `$EDITOR` — the same key
Claude Code's own prompt box binds it to, since it's the same box for the same purpose. The
tui hands over the screen and the keyboard, opens `$VISUAL`, `$EDITOR` or `vi` on what
you've typed so far, and takes back whatever you save, with the list drawn again around it.
An editor that quits without saving leaves the prompt as it was.

A long paste — over 800 characters or two line breaks — goes in as `[Pasted text #1 +40
lines]`, the marker Claude Code's own box puts in for one, so what you type around it stays
in view. The paste goes back in its place when the prompt is queued or its agent started, and
`$EDITOR` gets it in full, with the marker back over it if it comes back untouched. A paste
is text however long it is: a line break in one is a line break, not the `↵` that records the
prompt.

To see a long paste in the box, paste it again: the second paste replaces the marker with the
full text, as Claude Code's own box does. It only works while that marker is the last one in
the box and the paste is under 100,000 characters.

`n` is also vim's next-match key, and while a [search](search.md) pattern is in force that's
what it stays: `/`, then `n` and `N` to walk the matches, and `esc` to clear the pattern and
get the queue key back. The bar says which one is live.

## When its turn comes

Removing the worktree does *not* take the chain with it — it starts it. Every prompt that
was waiting on that worktree moves up to a row of its own, called `<new>` because its turn
has come and it hasn't got a worktree yet, with whatever was queued behind it still under
it:

```
    NAME                    BRANCH                    AGE  AGENT  TOPIC
  ✓ calm-baking-otter       worktree-calm-bak…-otter  1d   no     ⎇ Fix the flux capacitor
    <new>                                             2h          rebase onto main and open the PR
    ↳ <queued>                                        2h          then port the widget to the mobile layout
```

`space` (or a double-click) on a `<new>` row is where the worktree finally gets made: a
fresh one, opened as its own [Herdr](../herdr/index.md) workspace the way `c`
opens one — [`ccwt ws`](../herdr/ws.md) in its first tab, opened on its seed prompt with the
queued prompt and its title already typed in. Nothing runs yet: add a line, pick the effort,
and `↵` starts it in a tab of its own — `claude "<the prompt>"`, or whatever
[`task_command`](../configuration.md#task_command) in the config says, e.g.

```toml
task_command = "claude --permission-mode plan"
```

The prompt itself travels in a file — the pane is handed `claude "$(cat …)"`, and the file
deletes itself as it is read. That command is typed at the pane's shell prompt, and a
terminal takes about a kilobyte of a typed line before it drops the rest, which a prompt
worth queueing is easily longer than; a filename is the same hundred characters however long
the prompt is, and the shell never has to be taught to read back the quotes, newlines and
backslashes in it.

The row is then a worktree like any other, and the rest of the chain hangs off it, waiting
on the work that has just started rather than on the prompt that started it.

## Work that waits on nothing

A `<new>` row is also where work that isn't waiting for *anything* starts, and `n` with
nothing selected is how you write one down: there's no row to hang it off, so it goes on the
list as a `<new>` of its own straight away — a worktree that hasn't been made yet, with the
prompt to run in it when it is. `esc` is how you get back to nothing selected, and `space`
on the row makes the worktree, exactly as it does for one that got there by having its
worktree removed. Under [`-g`](../projects.md) this needs a project selected, its section
header being enough to say which repo the worktree is to be made in; with nothing selected
there the bar drops the key.

## Prompts from todography

`T` is where the prompt can come from when the repo keeps a [todography](https://github.com/mkmik/todography)
map: `todography next`, the wanted work whose needs are met, in a pane over the list. `c` on
an item opens the same box on its `t8y:` url, the title the url without the scheme, queued
where `n` would have queued it; [`ccwt ws`](../herdr/ws.md#seeding-agents) has the same key
for its seed prompt. `/` in the pane narrows its items to those holding what you type, `↵`
hands `c` back, and `esc` drops the filter before it closes the pane.

## Where the queue lives

The queue lives in `$XDG_STATE_HOME/ccwt/tasks.db` (`~/.local/state/ccwt/tasks.db` by
default) — one SQLite database for every project, so a prompt queued in a project's own tui
shows up in `tui -g` and the other way round, within a refresh interval either way.
The same database keeps a `seen` table: every repo and Claude Code worktree `ccwt` has been
run in or has made, with when it was first and last used and how many times — nothing reads
it yet.
