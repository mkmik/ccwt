#!/usr/bin/env bash
# `ccwt.new-worktree` action: create a worktree with ccwt from the repo the
# focused pane is sitting in, then hand the path to `herdr worktree open` so it
# lands in its own workspace.
set -euo pipefail

herdr="${HERDR_BIN_PATH:-herdr}"
ccwt="${CCWT_BIN:-ccwt}"

# ponytail: sed instead of a jq dependency — herdr prints compact JSON, and
# `"cwd":"` cannot match inside `"foreground_cwd":"` (the quote before the key
# is part of the pattern). Reach for jq if this ever needs nested lookups.
json_str() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"; }

if [ "${1:-}" = "--self-test" ]; then
  j='{"result":{"pane":{"pane_id":"w1:p1","cwd":"/repo","foreground_cwd":"/repo/sub"}}}'
  [ "$(json_str "$j" cwd)" = "/repo" ]
  [ "$(json_str "$j" foreground_cwd)" = "/repo/sub" ]
  [ -z "$(json_str '{"cwd":"/repo","foreground_cwd":null}' foreground_cwd)" ]
  echo ok
  exit 0
fi

# Actions run detached from any terminal, so a failure is only visible as a
# toast (and in `herdr plugin log list --plugin ccwt`).
fail() {
  "$herdr" notification show "ccwt: $1" --body "$2" >/dev/null 2>&1 || true
  printf '%s: %s\n' "$1" "$2" >&2
  exit 1
}

pane="$("$herdr" pane current ${HERDR_PANE_ID:+--pane "$HERDR_PANE_ID"})" ||
  fail "no pane" "could not resolve the focused pane"

# Where the user actually is: the shell's cwd when herdr can see it, the pane's otherwise.
cwd="$(json_str "$pane" foreground_cwd)"
[ -n "$cwd" ] || cwd="$(json_str "$pane" cwd)"
[ -n "$cwd" ] || fail "no working directory" "the focused pane does not report a cwd"

# --force-create: from inside a worktree, make a sibling one rather than
# returning the enclosing worktree.
path="$(cd "$cwd" && "$ccwt" new --force-create --path)" ||
  fail "ccwt new failed" "in $cwd — is ccwt on PATH and $cwd a git repo?"

# herdr only spawns worktree workspaces from the repo's parent workspace — a
# cwd inside a linked worktree gets rejected with `linked_worktree_source`. So
# hand it the enclosing repo root (`ccwt ..` strips .claude/worktrees/<name>),
# which is where ccwt put the new worktree anyway.
root="$(cd "$cwd" && "$ccwt" ..)" || fail "no repo root" "for $cwd"

# --label: herdr lists a worktree workspace under its repo by branch, so without
# one the sidebar reads "worktree-<name>" (or whatever branch_prefix says)
# instead of <name>. Safe to pin here because this path only ever opens a
# worktree it just created — a reopen would be overwriting a name the user may
# have changed.
"$herdr" worktree open --cwd "$root" --path "$path" --label "$(basename "$path")" --focus ||
  fail "worktree open failed" "$path"
