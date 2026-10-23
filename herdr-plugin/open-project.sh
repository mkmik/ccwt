#!/usr/bin/env bash
# `ccwt.open-project` action: `ccwt open`, which goes to the pane `ccwt -g` is
# running in and puts its project picker up there. Like new-worktree.sh, a
# failure is only visible as a toast.
set -uo pipefail

herdr="${HERDR_BIN_PATH:-herdr}"
if ! out="$("${CCWT_BIN:-ccwt}" open 2>&1)"; then
  "$herdr" notification show "ccwt: open failed" --body "$out" >/dev/null 2>&1 || true
  printf '%s\n' "$out" >&2
  exit 1
fi
