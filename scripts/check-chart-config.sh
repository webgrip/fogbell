#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
helm template fogbell deploy/charts/fogbell \
  --set flux.rbac=true --set tickets.clickupSecret=clickup --set 'roster.people={U012ABCDEF}' \
  --set config.tickets.clickup.list=901 >"$work/rendered.yaml"
awk '/^  config.yaml: \|$/{on=1; next} on && /^  [a-z]/{on=0} on{sub(/^    /, ""); print}' "$work/rendered.yaml" >"$work/config.yaml"
test -s "$work/config.yaml"
go run ./cmd/fogbell -check -config "$work/config.yaml"
