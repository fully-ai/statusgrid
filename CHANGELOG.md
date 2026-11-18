# Changelog

## [0.1.0] - 2026-11-18

### Added
- Multi-protocol availability checker (`http`, `https`, `tcp`, `dns`) measuring latency, HTTP status code validation, body substring assertions, TLS certificate expiry, and DNS record resolution.
- Central API + SQLite persistence (`modernc.org/sqlite`) for monitors, service groups, multi-region check results, automatic & manual incidents, incident timelines, maintenance windows, and subscriber notifications.
- Standalone distributed `statusgrid-probe` agent binary for regional edge polling (`us-east`, `eu-central`, `ap-south`, etc.).
- Operator dashboard and public `/status/{slug}` status pages with uptime percentages and SVG latency sparklines.
