package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fully-ai/statusgrid/internal/server"
	"github.com/fully-ai/statusgrid/internal/store"
)

func envOr(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := flag.String("addr", envOr("STATUSGRID_ADDR", "127.0.0.1:8094"), "Listen address")
	dataDir := flag.String("data", envOr("STATUSGRID_DATA_DIR", "./data"), "SQLite data directory")
	adminTok := flag.String("admin-token", envOr("STATUSGRID_ADMIN_TOKEN", ""), "Optional admin X-Admin-Token")
	probeTok := flag.String("probe-token", envOr("STATUSGRID_PROBE_TOKEN", ""), "Optional probe X-Probe-Token")
	region := flag.String("region", envOr("STATUSGRID_REGION", "local-primary"), "Built-in worker region")
	interval := flag.Duration("interval", 15*time.Second, "Built-in scheduler check interval (0 to disable)")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sqlite init error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := server.NewServer(st, *adminTok, *probeTok)
	if *interval > 0 {
		go func() {
			ticker := time.NewTicker(*interval)
			defer ticker.Stop()
			for range ticker.C {
				srv.RunCheckCycle(*region)
			}
		}()
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("StatusGrid server listening on http://%s (region=%s)\n", *addr, *region)
	if err := httpSrv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
