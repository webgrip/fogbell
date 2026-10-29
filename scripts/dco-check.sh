#!/usr/bin/env bash
set -euo pipefail
if [ $# -ne 2 ]; then
  echo "usage: dco-check.sh <base> <head>" >&2
  exit 2
fi
missing=0
for commit in $(git rev-list --no-merges "$1..$2"); do
  email=$(git log -1 --format=%ae "$commit")
  case "$email" in
    *"[bot]@users.noreply.github.com") continue ;;
  esac
  if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$commit" | grep -qiF "<$email>"; then
    echo "$(git log -1 --format='%h %s' "$commit"): no Signed-off-by for $email" >&2
    missing=1
  fi
done
if [ "$missing" -ne 0 ]; then
  echo "Sign off each commit with 'git commit -s' (see CONTRIBUTING.md). Fix existing commits with 'git rebase --signoff $1'." >&2
  exit 1
fi
