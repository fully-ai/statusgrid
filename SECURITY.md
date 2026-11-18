# Security Policy

## Reporting a Vulnerability

Please report vulnerabilities via GitHub Security Advisories without publicly disclosing exploit details prior to remediation.

## Controls Implemented

- **Separated Admin & Probe Tokens:** Administrative monitor/incident/maintenance mutations require `X-Admin-Token` (constant-time `subtle.ConstantTimeCompare`), while remote `statusgrid-probe` agents authenticate with a separate `STATUSGRID_PROBE_TOKEN`.
- **Safe Redirect & Response Limits:** HTTP(S) monitors disable unbounded body reads (`io.LimitReader`) and validate protocol schemes (`http`, `https`, `tcp`, `dns`).
- **XSS-Escaped Status Pages & Security Headers:** Public `/status/{slug}` pages and dashboard escape all dynamic text and enforce `X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY`.
