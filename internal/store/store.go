package store

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fully-ai/statusgrid/internal/checker"
	_ "modernc.org/sqlite"
)

type Incident struct {
	ID         string           `json:"id"`
	MonitorID  string           `json:"monitor_id"`
	Title      string           `json:"title"`
	State      string           `json:"state"` // investigating | identified | monitoring | resolved
	CreatedAt  string           `json:"created_at"`
	ResolvedAt string           `json:"resolved_at"`
	Updates    []IncidentUpdate `json:"updates,omitempty"`
}

type IncidentUpdate struct {
	ID         string `json:"id"`
	IncidentID string `json:"incident_id"`
	State      string `json:"state"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at"`
}

type MaintenanceWindow struct {
	ID        string `json:"id"`
	MonitorID string `json:"monitor_id"` // empty string applies globally
	Title     string `json:"title"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
	CreatedAt string `json:"created_at"`
}

type StatusPage struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

type Subscriber struct {
	ID         string `json:"id"`
	PageSlug   string `json:"page_slug"`
	Channel    string `json:"channel"` // webhook | email
	Target     string `json:"target"`
	CreatedAt  string `json:"created_at"`
}

type MonitorStats struct {
	MonitorID     string                `json:"monitor_id"`
	UptimePercent float64               `json:"uptime_percent"`
	TotalChecks   int                   `json:"total_checks"`
	FailedChecks  int                   `json:"failed_checks"`
	AvgLatencyMS  float64               `json:"avg_latency_ms"`
	RecentResults []checker.CheckResult `json:"recent_results"`
}

type Store struct {
	db         *sql.DB
	mu         sync.Mutex
	httpClient *http.Client
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "statusgrid.db"))
	if err != nil {
		return nil, err
	}
	schema := `
	PRAGMA journal_mode = WAL;
	PRAGMA foreign_keys = ON;
	CREATE TABLE IF NOT EXISTS monitors (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		service_group TEXT NOT NULL DEFAULT 'core',
		kind TEXT NOT NULL,
		target TEXT NOT NULL,
		region TEXT NOT NULL DEFAULT 'local-primary',
		interval_sec INTEGER NOT NULL DEFAULT 30,
		timeout_ms INTEGER NOT NULL DEFAULT 5000,
		expected_status INTEGER NOT NULL DEFAULT 200,
		body_contains TEXT NOT NULL DEFAULT '',
		expected_dns TEXT NOT NULL DEFAULT '',
		failure_threshold INTEGER NOT NULL DEFAULT 2,
		enabled INTEGER NOT NULL DEFAULT 1,
		current_state TEXT NOT NULL DEFAULT 'up',
		consecutive_failures INTEGER NOT NULL DEFAULT 0,
		last_latency_ms REAL NOT NULL DEFAULT 0,
		last_checked_at TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS check_results (
		id TEXT PRIMARY KEY,
		monitor_id TEXT NOT NULL,
		region TEXT NOT NULL,
		success INTEGER NOT NULL,
		status_code INTEGER NOT NULL,
		latency_ms REAL NOT NULL,
		tls_days_left INTEGER,
		error_message TEXT NOT NULL DEFAULT '',
		checked_at TEXT NOT NULL,
		FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_checks_mon ON check_results(monitor_id, checked_at DESC);
	CREATE TABLE IF NOT EXISTS incidents (
		id TEXT PRIMARY KEY,
		monitor_id TEXT NOT NULL,
		title TEXT NOT NULL,
		state TEXT NOT NULL,
		created_at TEXT NOT NULL,
		resolved_at TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS incident_updates (
		id TEXT PRIMARY KEY,
		incident_id TEXT NOT NULL,
		state TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY (incident_id) REFERENCES incidents(id) ON DELETE CASCADE
	);
	CREATE TABLE IF NOT EXISTS maintenance_windows (
		id TEXT PRIMARY KEY,
		monitor_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		starts_at TEXT NOT NULL,
		ends_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS status_pages (
		id TEXT PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS subscribers (
		id TEXT PRIMARY KEY,
		page_slug TEXT NOT NULL,
		channel TEXT NOT NULL,
		target TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS notification_deliveries (
		id TEXT PRIMARY KEY,
		subscriber_id TEXT NOT NULL,
		incident_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL
	);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, _ = db.Exec(
		`INSERT OR IGNORE INTO status_pages (id, slug, title, description, created_at) VALUES ('default', 'default', 'Service Status', 'Real-time service availability and incident updates', ?)`,
		time.Now().UTC().Format(time.RFC3339),
	)
	return &Store{db: db, httpClient: &http.Client{Timeout: 5 * time.Second}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Store) CreateMonitor(m checker.Monitor) (checker.Monitor, error) {
	if m.ID == "" {
		m.ID = newID()
	}
	if m.ServiceGroup == "" {
		m.ServiceGroup = "core"
	}
	if m.Region == "" {
		m.Region = "local-primary"
	}
	if m.IntervalSec <= 0 {
		m.IntervalSec = 30
	}
	if m.TimeoutMS <= 0 {
		m.TimeoutMS = 5000
	}
	if m.ExpectedStatus == 0 {
		m.ExpectedStatus = 200
	}
	if m.FailureThresh <= 0 {
		m.FailureThresh = 2
	}
	m.Enabled = true
	m.CurrentState = "up"
	_, err := s.db.Exec(
		`INSERT INTO monitors (id, name, service_group, kind, target, region, interval_sec, timeout_ms, expected_status, body_contains, expected_dns, failure_threshold, enabled, current_state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'up')`,
		m.ID, m.Name, m.ServiceGroup, m.Kind, m.Target, m.Region, m.IntervalSec, m.TimeoutMS, m.ExpectedStatus, m.BodyContains, m.ExpectedDNS, m.FailureThresh,
	)
	return m, err
}

func (s *Store) ListMonitors(regionFilter string) ([]checker.Monitor, error) {
	q := `SELECT id, name, service_group, kind, target, region, interval_sec, timeout_ms, expected_status, body_contains, expected_dns, failure_threshold, enabled, current_state, last_latency_ms, last_checked_at FROM monitors`
	var args []any
	if regionFilter != "" {
		q += " WHERE region = ? AND enabled = 1"
		args = append(args, regionFilter)
	}
	q += " ORDER BY service_group ASC, name ASC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []checker.Monitor
	for rows.Next() {
		var m checker.Monitor
		var en int
		if err := rows.Scan(&m.ID, &m.Name, &m.ServiceGroup, &m.Kind, &m.Target, &m.Region, &m.IntervalSec, &m.TimeoutMS, &m.ExpectedStatus, &m.BodyContains, &m.ExpectedDNS, &m.FailureThresh, &en, &m.CurrentState, &m.LastLatencyMS, &m.LastCheckedAt); err != nil {
			return nil, err
		}
		m.Enabled = en == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMonitor(id string) bool {
	res, err := s.db.Exec("DELETE FROM monitors WHERE id = ?", id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *Store) IsInMaintenance(monitorID string, at time.Time) bool {
	iso := at.UTC().Format(time.RFC3339)
	var cnt int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM maintenance_windows WHERE (monitor_id = '' OR monitor_id = ?) AND starts_at <= ? AND ends_at >= ?`,
		monitorID, iso, iso,
	).Scan(&cnt)
	return err == nil && cnt > 0
}

func (s *Store) RecordCheck(res checker.CheckResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if res.CheckedAt == "" {
		res.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	}
	succ := 0
	if res.Success {
		succ = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO check_results (id, monitor_id, region, success, status_code, latency_ms, tls_days_left, error_message, checked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), res.MonitorID, res.Region, succ, res.StatusCode, res.LatencyMS, res.TLSDaysLeft, res.ErrorMessage, res.CheckedAt,
	)
	if err != nil {
		return err
	}

	var name string
	var failThresh, consec int
	err = s.db.QueryRow("SELECT name, failure_threshold, consecutive_failures FROM monitors WHERE id = ?", res.MonitorID).Scan(&name, &failThresh, &consec)
	if err != nil {
		return nil
	}

	if s.IsInMaintenance(res.MonitorID, time.Now().UTC()) {
		_, _ = s.db.Exec("UPDATE monitors SET current_state = 'maintenance', last_latency_ms = ?, last_checked_at = ? WHERE id = ?", res.LatencyMS, res.CheckedAt, res.MonitorID)
		return nil
	}

	if res.Success {
		_, _ = s.db.Exec("UPDATE monitors SET current_state = 'up', consecutive_failures = 0, last_latency_ms = ?, last_checked_at = ? WHERE id = ?", res.LatencyMS, res.CheckedAt, res.MonitorID)
		var openID string
		if s.db.QueryRow("SELECT id FROM incidents WHERE monitor_id = ? AND state != 'resolved' ORDER BY created_at DESC LIMIT 1", res.MonitorID).Scan(&openID) == nil {
			now := time.Now().UTC().Format(time.RFC3339)
			_, _ = s.db.Exec("UPDATE incidents SET state = 'resolved', resolved_at = ? WHERE id = ?", now, openID)
			_, _ = s.db.Exec("INSERT INTO incident_updates (id, incident_id, state, message, created_at) VALUES (?, ?, 'resolved', 'Service check recovered.', ?)", newID(), openID, now)
			s.notifySubscribers(openID, name+" recovered")
		}
		return nil
	}

	consec++
	state := "degraded"
	if consec >= failThresh {
		state = "down"
	}
	_, _ = s.db.Exec("UPDATE monitors SET current_state = ?, consecutive_failures = ?, last_latency_ms = ?, last_checked_at = ? WHERE id = ?", state, consec, res.LatencyMS, res.CheckedAt, res.MonitorID)

	if consec >= failThresh {
		var existing string
		if s.db.QueryRow("SELECT id FROM incidents WHERE monitor_id = ? AND state != 'resolved' LIMIT 1", res.MonitorID).Scan(&existing) != nil {
			incID := newID()
			now := time.Now().UTC().Format(time.RFC3339)
			title := name + " outage (" + res.ErrorMessage + ")"
			_, _ = s.db.Exec("INSERT INTO incidents (id, monitor_id, title, state, created_at) VALUES (?, ?, ?, 'investigating', ?)", incID, res.MonitorID, title, now)
			_, _ = s.db.Exec("INSERT INTO incident_updates (id, incident_id, state, message, created_at) VALUES (?, ?, 'investigating', ?, ?)", newID(), incID, "Automated probe failure: "+res.ErrorMessage, now)
			s.notifySubscribers(incID, title)
		}
	}
	return nil
}

func (s *Store) notifySubscribers(incidentID, summary string) {
	rows, err := s.db.Query("SELECT id, channel, target FROM subscribers")
	if err != nil {
		return
	}
	defer rows.Close()
	type sub struct{ id, channel, target string }
	var subs []sub
	for rows.Next() {
		var x sub
		if rows.Scan(&x.id, &x.channel, &x.target) == nil {
			subs = append(subs, x)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, sb := range subs {
		status := "queued"
		if sb.channel == "webhook" && sb.target != "" {
			payload, _ := json.Marshal(map[string]string{"incident_id": incidentID, "message": summary, "sent_at": now})
			req, err := http.NewRequest(http.MethodPost, sb.target, bytes.NewReader(payload))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				if resp, derr := s.httpClient.Do(req); derr == nil {
					_ = resp.Body.Close()
					status = "sent"
				} else {
					status = "failed"
				}
			}
		}
		_, _ = s.db.Exec("INSERT INTO notification_deliveries (id, subscriber_id, incident_id, status, created_at) VALUES (?, ?, ?, ?, ?)", newID(), sb.id, incidentID, status, now)
	}
}

func (s *Store) MonitorStats(monitorID string) (MonitorStats, error) {
	st := MonitorStats{MonitorID: monitorID, UptimePercent: 100.0}
	var total, okCnt int
	var avgLat sql.NullFloat64
	if err := s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(success),0), AVG(latency_ms) FROM check_results WHERE monitor_id = ?", monitorID).Scan(&total, &okCnt, &avgLat); err != nil {
		return st, err
	}
	st.TotalChecks = total
	st.FailedChecks = total - okCnt
	if total > 0 {
		st.UptimePercent = (float64(okCnt) / float64(total)) * 100.0
	}
	if avgLat.Valid {
		st.AvgLatencyMS = avgLat.Float64
	}
	rows, err := s.db.Query("SELECT monitor_id, region, success, status_code, latency_ms, tls_days_left, error_message, checked_at FROM check_results WHERE monitor_id = ? ORDER BY checked_at DESC LIMIT 40", monitorID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var r checker.CheckResult
		var succ int
		var tlsDays sql.NullInt64
		if rows.Scan(&r.MonitorID, &r.Region, &succ, &r.StatusCode, &r.LatencyMS, &tlsDays, &r.ErrorMessage, &r.CheckedAt) == nil {
			r.Success = succ == 1
			if tlsDays.Valid {
				d := int(tlsDays.Int64)
				r.TLSDaysLeft = &d
			}
			st.RecentResults = append(st.RecentResults, r)
		}
	}
	return st, rows.Err()
}

func (s *Store) CreateIncident(monitorID, title, state, message string) (Incident, error) {
	if state == "" {
		state = "investigating"
	}
	inc := Incident{ID: newID(), MonitorID: monitorID, Title: title, State: state, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	_, err := s.db.Exec("INSERT INTO incidents (id, monitor_id, title, state, created_at) VALUES (?, ?, ?, ?, ?)", inc.ID, inc.MonitorID, inc.Title, inc.State, inc.CreatedAt)
	if err != nil {
		return inc, err
	}
	upd := IncidentUpdate{ID: newID(), IncidentID: inc.ID, State: state, Message: message, CreatedAt: inc.CreatedAt}
	_, _ = s.db.Exec("INSERT INTO incident_updates (id, incident_id, state, message, created_at) VALUES (?, ?, ?, ?, ?)", upd.ID, upd.IncidentID, upd.State, upd.Message, upd.CreatedAt)
	inc.Updates = []IncidentUpdate{upd}
	return inc, nil
}

func (s *Store) AddIncidentUpdate(incidentID, state, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	resolvedAt := ""
	if state == "resolved" {
		resolvedAt = now
	}
	res, err := s.db.Exec("UPDATE incidents SET state = ?, resolved_at = CASE WHEN ? != '' THEN ? ELSE resolved_at END WHERE id = ?", state, resolvedAt, resolvedAt, incidentID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	_, err = s.db.Exec("INSERT INTO incident_updates (id, incident_id, state, message, created_at) VALUES (?, ?, ?, ?, ?)", newID(), incidentID, state, message, now)
	return err
}

func (s *Store) ListIncidents() ([]Incident, error) {
	rows, err := s.db.Query("SELECT id, monitor_id, title, state, created_at, resolved_at FROM incidents ORDER BY created_at DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var inc Incident
		if rows.Scan(&inc.ID, &inc.MonitorID, &inc.Title, &inc.State, &inc.CreatedAt, &inc.ResolvedAt) == nil {
			urows, uerr := s.db.Query("SELECT id, incident_id, state, message, created_at FROM incident_updates WHERE incident_id = ? ORDER BY created_at ASC", inc.ID)
			if uerr == nil {
				for urows.Next() {
					var u IncidentUpdate
					if urows.Scan(&u.ID, &u.IncidentID, &u.State, &u.Message, &u.CreatedAt) == nil {
						inc.Updates = append(inc.Updates, u)
					}
				}
				_ = urows.Close()
			}
			out = append(out, inc)
		}
	}
	return out, rows.Err()
}

func (s *Store) CreateMaintenance(m MaintenanceWindow) (MaintenanceWindow, error) {
	m.ID = newID()
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec("INSERT INTO maintenance_windows (id, monitor_id, title, starts_at, ends_at, created_at) VALUES (?, ?, ?, ?, ?, ?)", m.ID, m.MonitorID, m.Title, m.StartsAt, m.EndsAt, m.CreatedAt)
	return m, err
}

func (s *Store) ListMaintenance() ([]MaintenanceWindow, error) {
	rows, err := s.db.Query("SELECT id, monitor_id, title, starts_at, ends_at, created_at FROM maintenance_windows ORDER BY starts_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceWindow
	for rows.Next() {
		var m MaintenanceWindow
		if rows.Scan(&m.ID, &m.MonitorID, &m.Title, &m.StartsAt, &m.EndsAt, &m.CreatedAt) == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetStatusPage(slug string) (StatusPage, error) {
	var p StatusPage
	err := s.db.QueryRow("SELECT id, slug, title, description, created_at FROM status_pages WHERE slug = ?", slug).Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &p.CreatedAt)
	return p, err
}

func (s *Store) CreateSubscriber(slug, channel, target string) (Subscriber, error) {
	sub := Subscriber{ID: newID(), PageSlug: slug, Channel: channel, Target: target, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	_, err := s.db.Exec("INSERT INTO subscribers (id, page_slug, channel, target, created_at) VALUES (?, ?, ?, ?, ?)", sub.ID, sub.PageSlug, sub.Channel, sub.Target, sub.CreatedAt)
	return sub, err
}

func (s *Store) NotificationCount() int {
	var cnt int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM notification_deliveries").Scan(&cnt)
	return cnt
}
