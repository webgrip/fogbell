#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
test -z "$(gofmt -l .)"
go vet ./...
go build ./...
if command -v cc >/dev/null 2>&1; then
  CGO_ENABLED=1 go test -count=1 -race ./...
else
  echo "verify: no C compiler, so the tests run without -race" >&2
  go test -count=1 ./...
fi
golangci-lint run ./...
helm lint deploy/charts/fogbell
helm lint deploy/charts/fogbell --set flux.rbac=true --set tickets.clickupSecret=clickup --set serviceMonitor.enabled=true --set 'roster.people={U012ABCDEF}'
bash scripts/check-chart-config.sh
go run ./cmd/fogbell -check -config examples/config.yaml
shellcheck scripts/*.sh
