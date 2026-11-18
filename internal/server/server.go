package server

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/fully-ai/statusgrid/internal/checker"
	"github.com/fully-ai/statusgrid/internal/store"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	store      *store.Store
	adminToken string
	probeToken string
}

func NewServer(st *store.Store, adminToken, probeToken string) *Server {
	return &Server{store: st, adminToken: adminToken, probeToken: probeToken}
}

func checkHeaderToken(r *http.Request, headerName, expected string) bool {
	if expected == "" {
		return true
	}
	got := r.Header.Get(headerName)
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) RunCheckCycle(region string) int {
	monitors, err := s.store.ListMonitors(region)
	if err != nil {
		return 0
	}
	count := 0
	for _, m := range monitors {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res := checker.RunCheck(ctx, m, region)
		cancel()
		_ = s.store.RecordCheck(res)
		count++
	}
	return count
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/status/"), "/")
		if slug == "" {
			slug = "default"
		}
		page, err := s.store.GetStatusPage(slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		monitors, _ := s.store.ListMonitors("")
		incidents, _ := s.store.ListIncidents()
		if r.URL.Query().Get("format") == "json" {
			writeJSON(w, http.StatusOK, map[string]any{"page": page, "monitors": monitors, "incidents": incidents})
			return
		}
		var rows strings.Builder
		for _, m := range monitors {
			rows.WriteString("<li><strong>" + html.EscapeString(m.Name) + "</strong> (" + html.EscapeString(m.ServiceGroup) + ") — <code>" + html.EscapeString(m.CurrentState) + "</code></li>")
		}
		var incRows strings.Builder
		for _, inc := range incidents {
			incRows.WriteString("<li><strong>[" + html.EscapeString(inc.State) + "]</strong> " + html.EscapeString(inc.Title) + "</li>")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><head><meta charset='utf-8'><title>" + html.EscapeString(page.Title) + "</title>" +
			"<style>body{font-family:system-ui,sans-serif;background:#0d1117;color:#e6edf3;max-width:780px;margin:32px auto;padding:0 16px;}li{margin:8px 0;}</style></head><body>" +
			"<h1>" + html.EscapeString(page.Title) + "</h1><p>" + html.EscapeString(page.Description) + "</p>" +
			"<h2>Services</h2><ul>" + rows.String() + "</ul><h2>Incidents</h2><ul>" + incRows.String() + "</ul></body></html>"))
	})

	mux.HandleFunc("/api/status-pages/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/subscribe") {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/status-pages/"), "/subscribe")
		var req struct {
			Channel string `json:"channel"`
			Target  string `json:"target"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || req.Target == "" {
			writeErr(w, http.StatusBadRequest, "channel and target required")
			return
		}
		if req.Channel == "" {
			req.Channel = "webhook"
		}
		sub, err := s.store.CreateSubscriber(slug, req.Channel, req.Target)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sub)
	})

	mux.HandleFunc("/api/monitors", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list, err := s.store.ListMonitors(r.URL.Query().Get("region"))
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"monitors": list})
			return
		}
		if r.Method == http.MethodPost {
			if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var m checker.Monitor
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&m); err != nil || m.Name == "" || m.Target == "" {
				writeErr(w, http.StatusBadRequest, "valid monitor name, kind, and target required")
				return
			}
			created, err := s.store.CreateMonitor(m)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, created)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})

	mux.HandleFunc("/api/monitors/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/monitors/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			writeErr(w, http.StatusBadRequest, "missing monitor id")
			return
		}
		id := parts[0]
		if len(parts) == 2 && parts[1] == "stats" && r.Method == http.MethodGet {
			st, err := s.store.MonitorStats(id)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, st)
			return
		}
		if len(parts) == 2 && parts[1] == "check" && r.Method == http.MethodPost {
			if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			mons, _ := s.store.ListMonitors("")
			for _, m := range mons {
				if m.ID == id {
					res := checker.RunCheck(r.Context(), m, m.Region)
					_ = s.store.RecordCheck(res)
					writeJSON(w, http.StatusOK, res)
					return
				}
			}
			writeErr(w, http.StatusNotFound, "monitor not found")
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if !s.store.DeleteMonitor(id) {
				writeErr(w, http.StatusNotFound, "monitor not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		writeErr(w, http.StatusNotFound, "unknown endpoint")
	})

	mux.HandleFunc("/api/incidents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list, err := s.store.ListIncidents()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"incidents": list})
			return
		}
		if r.Method == http.MethodPost {
			if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var req struct {
				MonitorID string `json:"monitor_id"`
				Title     string `json:"title"`
				State     string `json:"state"`
				Message   string `json:"message"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.MonitorID == "" || req.Title == "" {
				writeErr(w, http.StatusBadRequest, "monitor_id and title required")
				return
			}
			inc, err := s.store.CreateIncident(req.MonitorID, req.Title, req.State, req.Message)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, inc)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})

	mux.HandleFunc("/api/incidents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/updates") {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		incID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/incidents/"), "/updates")
		var req struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.State == "" || req.Message == "" {
			writeErr(w, http.StatusBadRequest, "state and message required")
			return
		}
		if err := s.store.AddIncidentUpdate(incID, req.State, req.Message); err != nil {
			writeErr(w, http.StatusNotFound, "incident not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	})

	mux.HandleFunc("/api/maintenance", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list, err := s.store.ListMaintenance()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"windows": list})
			return
		}
		if r.Method == http.MethodPost {
			if !checkHeaderToken(r, "X-Admin-Token", s.adminToken) {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var mw store.MaintenanceWindow
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&mw); err != nil || mw.Title == "" || mw.StartsAt == "" || mw.EndsAt == "" {
				writeErr(w, http.StatusBadRequest, "title, starts_at, ends_at required")
				return
			}
			created, err := s.store.CreateMaintenance(mw)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, created)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})

	// Distributed probe agent protocol
	mux.HandleFunc("/api/probe/jobs", func(w http.ResponseWriter, r *http.Request) {
		if !checkHeaderToken(r, "X-Probe-Token", s.probeToken) {
			writeErr(w, http.StatusUnauthorized, "unauthorized probe")
			return
		}
		region := r.URL.Query().Get("region")
		mons, err := s.store.ListMonitors(region)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": mons})
	})

	mux.HandleFunc("/api/probe/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkHeaderToken(r, "X-Probe-Token", s.probeToken) {
			writeErr(w, http.StatusUnauthorized, "unauthorized probe")
			return
		}
		var results []checker.CheckResult
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&results); err != nil {
			writeErr(w, http.StatusBadRequest, "expected JSON array of check results")
			return
		}
		for _, res := range results {
			_ = s.store.RecordCheck(res)
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"recorded": len(results)})
	})

	return withHeaders(mux)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
