package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fully-ai/statusgrid/internal/checker"
)

func envOr(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func runOnce(client *http.Client, serverURL, token, region string) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/api/probe/jobs?region="+region, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Probe-Token", token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fetch jobs failed (%d)", resp.StatusCode)
	}
	var payload struct {
		Jobs []checker.Monitor `json:"jobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if len(payload.Jobs) == 0 {
		return nil
	}
	results := make([]checker.CheckResult, 0, len(payload.Jobs))
	for _, job := range payload.Jobs {
		results = append(results, checker.RunCheck(context.Background(), job, region))
	}
	raw, _ := json.Marshal(results)
	postReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(serverURL, "/")+"/api/probe/report", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	postReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		postReq.Header.Set("X-Probe-Token", token)
	}
	postResp, err := client.Do(postReq)
	if err != nil {
		return err
	}
	_ = postResp.Body.Close()
	return nil
}

func main() {
	serverURL := flag.String("server", envOr("STATUSGRID_SERVER", "http://127.0.0.1:8094"), "Central StatusGrid API URL")
	token := flag.String("token", envOr("STATUSGRID_PROBE_TOKEN", ""), "Shared probe token")
	region := flag.String("region", envOr("STATUSGRID_REGION", "eu-central"), "Probe region identifier")
	interval := flag.Duration("interval", 15*time.Second, "Polling interval")
	once := flag.Bool("once", false, "Execute a single probe cycle and exit")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second}
	if *once {
		if err := runOnce(client, *serverURL, *token, *region); err != nil {
			fmt.Fprintf(os.Stderr, "probe error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("StatusGrid probe running (region=%s, server=%s)\n", *region, *serverURL)
	for {
		if err := runOnce(client, *serverURL, *token, *region); err != nil {
			fmt.Fprintf(os.Stderr, "probe error: %v\n", err)
		}
		time.Sleep(*interval)
	}
}
