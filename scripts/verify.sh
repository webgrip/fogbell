#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
test -z "$(gofmt -l .)"
go vet ./...
go build ./...
go test -count=1 -race ./...
golangci-lint run ./...
helm lint deploy/charts/fogbell
helm lint deploy/charts/fogbell --set flux.rbac=true --set tickets.clickupSecret=clickup --set serviceMonitor.enabled=true --set 'roster.people={U012ABCDEF}'
bash scripts/check-chart-config.sh
go run ./cmd/fogbell -check -config examples/config.yaml
actionlint
shellcheck scripts/*.sh
