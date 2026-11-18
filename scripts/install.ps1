$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)
New-Item -ItemType Directory -Force -Path bin | Out-Null
go mod download
go build -o bin/statusgrid-server.exe ./cmd/statusgrid-server
go build -o bin/statusgrid-probe.exe ./cmd/statusgrid-probe
