# StatusGrid Architecture

1. **Central API & SQLite Store (`internal/store/store.go`, `internal/server/`)**: Persists external service monitors (`http`, `https`, `tcp`, `dns`), regional check histories, incident state machines + timeline updates, maintenance windows, public status pages, and subscriber webhook deliveries.
2. **Protocol Checker (`internal/checker/checker.go`)**: Executes HTTP/HTTPS status, body assertion & TLS certificate expiry checks, TCP port dial latency checks, and DNS host record assertions.
3. **Distributed Regional Probes (`cmd/statusgrid-probe/main.go`)**: Edge agents poll `/api/probe/jobs?region=<region>` using `X-Probe-Token` and push batch check outcomes to `/api/probe/report`.
