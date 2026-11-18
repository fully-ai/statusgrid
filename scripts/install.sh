#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go mod download
go build -o bin/statusgrid-server ./cmd/statusgrid-server
go build -o bin/statusgrid-probe ./cmd/statusgrid-probe
