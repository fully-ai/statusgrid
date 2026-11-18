package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fully-ai/statusgrid/internal/checker"
	"github.com/fully-ai/statusgrid/internal/store"
)

func doReq(t *testing.T, h http.Handler, method, path, headerK, headerV string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if headerK != "" {
		req.Header.Set(headerK, headerV)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestStatusGridProbesIncidentsMaintenanceAndSubscribers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := NewServer(st, "adm-tok", "probe-tok")
	h := srv.Handler()

	// Real HTTP target that flips from 200 OK -> 500 -> 200 OK
	var healthy atomic.Bool
	healthy.Store(true)
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer targetSrv.Close()

	// Real TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	// Webhook subscriber server
	var webhookHits atomic.Int32
	subSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer subSrv.Close()

	// Subscribe to default status page
	wSub := doReq(t, h, http.MethodPost, "/api/status-pages/default/subscribe", "", "", map[string]string{
		"channel": "webhook", "target": subSrv.URL,
	})
	if wSub.Code != http.StatusCreated {
		t.Fatalf("subscribe failed: %d", wSub.Code)
	}

	// Register HTTP + TCP + DNS monitors
	wMon := doReq(t, h, http.MethodPost, "/api/monitors", "X-Admin-Token", "adm-tok", map[string]any{
		"name": "API Gateway", "kind": "http", "target": targetSrv.URL,
		"region": "eu-central", "expected_status": 200, "body_contains": `"status":"ok"`, "failure_threshold": 2,
	})
	if wMon.Code != http.StatusCreated {
		t.Fatalf("create monitor failed: %d %s", wMon.Code, wMon.Body.String())
	}
	var httpMon checker.Monitor
	_ = json.Unmarshal(wMon.Body.Bytes(), &httpMon)

	doReq(t, h, http.MethodPost, "/api/monitors", "X-Admin-Token", "adm-tok", map[string]any{
		"name": "Redis TCP", "kind": "tcp", "target": ln.Addr().String(), "region": "eu-central",
	})
	dnsRes := checker.RunCheck(context.Background(), checker.Monitor{ID: "dns1", Kind: "dns", Target: "localhost", TimeoutMS: 2000}, "eu-central")
	if !dnsRes.Success {
		t.Fatalf("expected localhost DNS check to succeed, got %s", dnsRes.ErrorMessage)
	}

	// Probe jobs polling & reporting
	wJobs := doReq(t, h, http.MethodGet, "/api/probe/jobs?region=eu-central", "X-Probe-Token", "probe-tok", nil)
	if wJobs.Code != http.StatusOK || !strings.Contains(wJobs.Body.String(), "API Gateway") {
		t.Fatalf("probe jobs failed: %d %s", wJobs.Code, wJobs.Body.String())
	}

	// 1st healthy run
	if cnt := srv.RunCheckCycle("eu-central"); cnt != 2 {
		t.Fatalf("expected 2 checks executed, got %d", cnt)
	}

	// Trigger 2 consecutive HTTP failures -> opens incident + notifies subscriber
	healthy.Store(false)
	srv.RunCheckCycle("eu-central")
	srv.RunCheckCycle("eu-central")

	wInc := doReq(t, h, http.MethodGet, "/api/incidents", "", "", nil)
	if !strings.Contains(wInc.Body.String(), `"state":"investigating"`) {
		t.Fatalf("expected open investigating incident, got %s", wInc.Body.String())
	}

	// Recover -> auto-resolves incident + notifies subscriber again
	healthy.Store(true)
	srv.RunCheckCycle("eu-central")
	wInc2 := doReq(t, h, http.MethodGet, "/api/incidents", "", "", nil)
	if !strings.Contains(wInc2.Body.String(), `"state":"resolved"`) {
		t.Fatalf("expected auto-resolved incident, got %s", wInc2.Body.String())
	}
	if webhookHits.Load() < 2 {
		t.Fatalf("expected >= 2 subscriber webhook notifications (open + resolved), got %d", webhookHits.Load())
	}

	// Maintenance window puts monitor into maintenance state
	doReq(t, h, http.MethodPost, "/api/maintenance", "X-Admin-Token", "adm-tok", map[string]string{
		"monitor_id": httpMon.ID,
		"title":      "DB Upgrade",
		"starts_at":  time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
		"ends_at":    time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
	})
	srv.RunCheckCycle("eu-central")
	wPage := doReq(t, h, http.MethodGet, "/status/default?format=json", "", "", nil)
	if wPage.Code != http.StatusOK || !strings.Contains(wPage.Body.String(), `"current_state":"maintenance"`) {
		t.Fatalf("expected maintenance state on status page, got %s", wPage.Body.String())
	}
}
