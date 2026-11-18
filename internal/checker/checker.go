package checker

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Monitor struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ServiceGroup    string `json:"service_group"`
	Kind            string `json:"kind"` // http | https | tcp | dns
	Target          string `json:"target"`
	Region          string `json:"region"`
	IntervalSec     int    `json:"interval_sec"`
	TimeoutMS       int    `json:"timeout_ms"`
	ExpectedStatus  int    `json:"expected_status"`
	BodyContains    string `json:"body_contains"`
	ExpectedDNS     string `json:"expected_dns"`
	FailureThresh   int    `json:"failure_threshold"`
	Enabled         bool   `json:"enabled"`
	CurrentState    string `json:"current_state"` // up | degraded | down | maintenance
	LastLatencyMS   float64 `json:"last_latency_ms"`
	LastCheckedAt   string `json:"last_checked_at"`
}

type CheckResult struct {
	MonitorID    string  `json:"monitor_id"`
	Region       string  `json:"region"`
	Success      bool    `json:"success"`
	StatusCode   int     `json:"status_code"`
	LatencyMS    float64 `json:"latency_ms"`
	TLSDaysLeft  *int    `json:"tls_days_left,omitempty"`
	ErrorMessage string  `json:"error_message"`
	CheckedAt    string  `json:"checked_at"`
}

func RunCheck(ctx context.Context, m Monitor, region string) CheckResult {
	if region == "" {
		region = m.Region
	}
	timeout := time.Duration(m.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	res := CheckResult{
		MonitorID: m.ID,
		Region:    region,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	switch strings.ToLower(m.Kind) {
	case "http", "https":
		checkHTTP(cctx, m, &res)
	case "tcp":
		checkTCP(cctx, m, &res)
	case "dns":
		checkDNS(cctx, m, &res)
	default:
		res.ErrorMessage = "unsupported monitor kind: " + m.Kind
	}
	res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
	return res
}

func checkHTTP(ctx context.Context, m Monitor, res *CheckResult) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Target, nil)
	if err != nil {
		res.ErrorMessage = err.Error()
		return
	}
	req.Header.Set("User-Agent", "StatusGrid-Probe/0.1.0")
	resp, err := client.Do(req)
	if err != nil {
		res.ErrorMessage = err.Error()
		return
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		days := int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
		res.TLSDaysLeft = &days
	}

	want := m.ExpectedStatus
	if want == 0 {
		want = 200
	}
	if resp.StatusCode != want {
		res.ErrorMessage = fmt.Sprintf("expected HTTP %d, got %d", want, resp.StatusCode)
		return
	}
	if m.BodyContains != "" {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		if !strings.Contains(string(raw), m.BodyContains) {
			res.ErrorMessage = fmt.Sprintf("response body missing %q", m.BodyContains)
			return
		}
	}
	res.Success = true
}

func checkTCP(ctx context.Context, m Monitor, res *CheckResult) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", m.Target)
	if err != nil {
		res.ErrorMessage = err.Error()
		return
	}
	_ = conn.Close()
	res.Success = true
}

func checkDNS(ctx context.Context, m Monitor, res *CheckResult) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, m.Target)
	if err != nil {
		res.ErrorMessage = err.Error()
		return
	}
	if len(addrs) == 0 {
		res.ErrorMessage = "no DNS records returned"
		return
	}
	if m.ExpectedDNS != "" {
		found := false
		for _, a := range addrs {
			if a == m.ExpectedDNS {
				found = true
				break
			}
		}
		if !found {
			res.ErrorMessage = fmt.Sprintf("expected DNS %q, got %v", m.ExpectedDNS, addrs)
			return
		}
	}
	res.Success = true
}
