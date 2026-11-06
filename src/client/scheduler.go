package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// schedulerTask mirrors one scheduler task as returned by the server
// (GET /api/{version}/server/schedulers).
type schedulerTask struct {
	TaskID     string    `json:"task_id" yaml:"task_id"`
	TaskName   string    `json:"task_name" yaml:"task_name"`
	Schedule   string    `json:"schedule" yaml:"schedule"`
	LastRun    time.Time `json:"last_run" yaml:"last_run"`
	LastStatus string    `json:"last_status" yaml:"last_status"`
	LastError  string    `json:"last_error" yaml:"last_error"`
	NextRun    time.Time `json:"next_run" yaml:"next_run"`
	RunCount   int64     `json:"run_count" yaml:"run_count"`
	FailCount  int64     `json:"fail_count" yaml:"fail_count"`
	Enabled    bool      `json:"enabled" yaml:"enabled"`
}

// schedulerHistory is one execution record.
type schedulerHistory struct {
	TaskID     string    `json:"task_id" yaml:"task_id"`
	StartedAt  time.Time `json:"started_at" yaml:"started_at"`
	FinishedAt time.Time `json:"finished_at" yaml:"finished_at"`
	Status     string    `json:"status" yaml:"status"`
	ErrorMsg   string    `json:"error_msg" yaml:"error_msg"`
	DurationMS int64     `json:"duration_ms" yaml:"duration_ms"`
}

// schedulerStatusSymbol maps a task status to the PART 18 legend symbols.
func schedulerStatusSymbol(status string) string {
	switch status {
	case "success":
		return "✓"
	case "running":
		return "●"
	case "failed":
		return "✗"
	case "skipped":
		return "◐"
	default:
		return "○"
	}
}

// schedulerTime renders a timestamp or "-" when it is unset.
func schedulerTime(ts time.Time) string {
	if ts.IsZero() || ts.Unix() <= 0 {
		return "-"
	}
	return ts.Local().Format("2006-01-02 15:04:05")
}

// schedulerCall performs one scheduler API request and returns the decoded
// "data" payload. Auth, not-found and connection failures exit with the
// PART 32 exit codes.
func (c *client) schedulerCall(method, path, id string) json.RawMessage {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, c.url(path), body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_build_request", "command", "scheduler", "error", err))
		os.Exit(exitGeneral)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}
	c.setAuth(req)
	resp, err := c.doWithRetry(&http.Client{Timeout: c.clientTimeout()}, req)
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		printAuthError()
	case resp.StatusCode == http.StatusNotFound:
		printNotFoundError(id)
	case resp.StatusCode == http.StatusServiceUnavailable:
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("sched_unavailable"))
		os.Exit(exitGeneral)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_server_returned", "command", "scheduler", "status", resp.StatusCode))
		os.Exit(exitGeneral)
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}
	if c.outputFormat == "json" {
		_, _ = os.Stdout.Write(append(bytes.TrimSpace(raw), '\n'))
		os.Exit(0)
	}
	return env.Data
}

// cmdScheduler implements `scheduler {list|show|run|enable|disable|history}`
// (PART 18 → CLI Commands) against the server scheduler API.
func (c *client) cmdScheduler(args []string) {
	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	outputFlag := fs.String("output", c.outputFormat, "output format: json, yaml, table, plain, csv")
	jsonFlag := fs.Bool("json", false, "alias for --output json")
	if err := fs.Parse(reorderCommandArgs(args, "--output")); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_flags", "command", "scheduler", "error", err))
		os.Exit(exitUsage)
	}
	c.outputFormat = resolveOutputFormat(*jsonFlag, *outputFlag)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("sched_usage"))
		os.Exit(exitUsage)
	}
	action := fs.Arg(0)
	id := fs.Arg(1)
	base := "/api/" + c.apiVer() + "/server/schedulers"
	switch action {
	case "list", "ls":
		c.schedulerList(base)
	case "show", "run", "enable", "disable", "history":
		if id == "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("sched_id_required", "action", action))
			os.Exit(exitUsage)
		}
		path := base + "/" + url.PathEscape(id)
		switch action {
		case "show":
			c.schedulerShow(path, id)
		case "history":
			c.schedulerHistory(path+"/history", id)
		default:
			c.schedulerCall(http.MethodPost, path+"/"+action, id)
			if !c.quiet {
				fmt.Println(tf("sched_"+action+"_ok", "id", id))
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("sched_usage"))
		os.Exit(exitUsage)
	}
}

// emitStructured prints v as YAML or CSV when the output format asks for it
// and reports whether it handled the output.
func (c *client) emitStructured(v interface{}, headers []string, rows [][]string) bool {
	switch c.outputFormat {
	case "yaml":
		out, err := yaml.Marshal(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
			os.Exit(exitGeneral)
		}
		_ = writePagedOutput(out, c.pager)
		return true
	case "csv":
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write(headers)
		_ = w.WriteAll(rows)
		w.Flush()
		_ = writePagedOutput(buf.Bytes(), c.pager)
		return true
	}
	return false
}

func (c *client) schedulerList(base string) {
	var tasks []schedulerTask
	if err := json.Unmarshal(c.schedulerCall(http.MethodGet, base, ""), &tasks); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}
	headers := []string{t("sched_col_status"), t("sched_col_task"), t("sched_col_schedule"), t("sched_col_last_run"), t("sched_col_next_run")}
	rows := make([][]string, 0, len(tasks))
	for _, tk := range tasks {
		status := tk.LastStatus
		if !tk.Enabled {
			status = "pending"
		}
		rows = append(rows, []string{schedulerStatusSymbol(status), tk.TaskID, tk.Schedule, schedulerTime(tk.LastRun), schedulerTime(tk.NextRun)})
	}
	if c.emitStructured(tasks, headers, rows) {
		return
	}
	if len(rows) == 0 {
		fmt.Println(t("sched_none"))
		return
	}
	renderBorderedTable(os.Stdout, headers, rows)
}

func (c *client) schedulerShow(path, id string) {
	var tk schedulerTask
	if err := json.Unmarshal(c.schedulerCall(http.MethodGet, path, id), &tk); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}
	enabled := t("sched_no")
	if tk.Enabled {
		enabled = t("sched_yes")
	}
	headers := []string{t("sched_col_task"), t("sched_col_enabled"), t("sched_col_schedule"), t("sched_col_status"), t("sched_col_last_run"), t("sched_col_next_run"), t("sched_col_runs"), t("sched_col_failed"), t("sched_col_error")}
	row := []string{tk.TaskID, enabled, tk.Schedule, schedulerStatusSymbol(tk.LastStatus) + " " + tk.LastStatus, schedulerTime(tk.LastRun), schedulerTime(tk.NextRun),
		fmt.Sprint(tk.RunCount), fmt.Sprint(tk.FailCount), tk.LastError}
	if c.emitStructured(tk, headers, [][]string{row}) {
		return
	}
	for i, h := range headers {
		fmt.Printf("%-14s %s\n", h+":", row[i])
	}
}

func (c *client) schedulerHistory(path, id string) {
	var hist []schedulerHistory
	if err := json.Unmarshal(c.schedulerCall(http.MethodGet, path, id), &hist); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}
	headers := []string{t("sched_col_started"), t("sched_col_duration"), t("sched_col_status"), t("sched_col_error")}
	rows := make([][]string, 0, len(hist))
	for _, h := range hist {
		rows = append(rows, []string{schedulerTime(h.StartedAt), (time.Duration(h.DurationMS) * time.Millisecond).String(), schedulerStatusSymbol(h.Status) + " " + h.Status, h.ErrorMsg})
	}
	if c.emitStructured(hist, headers, rows) {
		return
	}
	if len(rows) == 0 {
		fmt.Println(tf("sched_history_none", "id", id))
		return
	}
	renderBorderedTable(os.Stdout, headers, rows)
}
