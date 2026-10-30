#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
helm template fogbell deploy/charts/fogbell \
  --set flux.rbac=true --set tickets.clickupSecret=clickup --set 'roster.people={U012ABCDEF}' \
  --set config.tickets.clickup.list=901 --set investigate.ploegSecret=ploeg --set config.investigate.ploeg.url=http://ploeg:8080 --set config.investigate.ploeg.team=sre --set config.investigate.ploeg.target=cluster --set 'extraSecrets[0].secret=ev' --set 'extraSecrets[0].mountPath=/var/run/fogbell/events' >"$work/rendered.yaml"
awk '/^  config.yaml: \|$/{on=1; next} on && /^  [a-z]/{on=0} on{sub(/^    /, ""); print}' "$work/rendered.yaml" >"$work/config.yaml"
test -s "$work/config.yaml"
go run ./cmd/fogbell -check -config "$work/config.yaml"
