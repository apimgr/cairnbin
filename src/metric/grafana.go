package metric

// Grafana dashboard definition (AI.md PART 20).
//
// The grafana metrics service returns a complete, importable dashboard
// definition covering every metric category this project requires: HTTP,
// database, cache, scheduler, system, and business. The datasource is left as a
// template variable so the dashboard imports cleanly against any Prometheus
// datasource the operator already has, rather than hard-coding a UID that
// only exists on this server.

// grafanaDashboardUID is the dashboard UID. Stable so a re-import updates the
// existing dashboard in place instead of creating a duplicate.
const grafanaDashboardUID = "cairnbin-overview"

// grafanaDS is the datasource template variable. Every panel query references
// it, so importing against a different Prometheus datasource is a one-field
// change at the dashboard level.
const grafanaDS = "DS_PROMETHEUS"

// ds returns the datasource ref for a panel, pointing at the template variable.
func ds() map[string]any {
	return map[string]any{
		"type":       "prometheus",
		"uid":        grafanaDS,
		"datasource": map[string]any{"type": "prometheus", "uid": grafanaDS},
		"hide":       false,
		"editorMode": "code",
		"range":      true,
		"exemplar":   true,
		"interval":   "",
	}
}

// target builds one PromQL query panel target, returned as the string form
// Grafana accepts under "expr".
func target(expr string, legend string) map[string]any {
	return map[string]any{
		"datasource":   ds(),
		"editorMode":   "code",
		"expr":         expr,
		"legendFormat": legend,
		"range":        true,
		"refId":        "A",
	}
}

// panel is a time-series panel spanning the full 24-column grid width, one row
// per metric category section. Grafana lays panels out by gridPos, so each
// panel gets its own row band.
func panel(id, row int, title, unit string, targets ...map[string]any) map[string]any {
	ts := append(make([]map[string]any, 0, len(targets)), targets...)
	return map[string]any{
		"id":          id,
		"type":        "timeseries",
		"title":       title,
		"description": title,
		"datasource":  ds(),
		"gridPos":     map[string]any{"h": 8, "w": 24, "x": 0, "y": row},
		"fieldConfig": map[string]any{
			"defaults": map[string]any{
				"unit": unit,
				"custom": map[string]any{
					"drawStyle":    "line",
					"lineWidth":    1,
					"fillOpacity":  10,
					"showPoints":   "never",
					"spanNulls":    true,
					"stacking":     map[string]any{"mode": "none", "group": "A"},
					"gradientMode": "none",
				},
				"color": map[string]any{"mode": "palette-classic"},
			},
			"overrides": []any{},
		},
		"options": map[string]any{
			"legend": map[string]any{
				"displayMode": "list",
				"placement":   "bottom",
				"calcs":       []string{"lastNotNull", "max"},
				"showLegend":  true,
			},
			"tooltip": map[string]any{"mode": "multi", "sort": "desc"},
		},
		"targets": ts,
	}
}

// statPanel is a single-value stat panel, used for gauges that are read as a
// single number rather than a rate over time.
func statPanel(id, row int, title, unit string, targets ...map[string]any) map[string]any {
	p := panel(id, row, title, unit, targets...)
	p["type"] = "stat"
	p["fieldConfig"].(map[string]any)["options"] = map[string]any{
		"reduceOptions": map[string]any{
			"calcs":  []string{"lastNotNull"},
			"fields": "",
			"values": false,
		},
		"colorMode":   "value",
		"graphMode":   "area",
		"textMode":    "auto",
		"justifyMode": "auto",
	}
	delete(p, "options")
	return p
}

// rowPanel is a collapsed section header.
func rowPanel(id, row int, title string) map[string]any {
	return map[string]any{
		"id":         id,
		"type":       "row",
		"title":      title,
		"collapsed":  false,
		"gridPos":    map[string]any{"h": 1, "w": 24, "x": 0, "y": row},
		"panels":     []any{},
		"datasource": ds(),
	}
}

// Dashboard returns the Grafana dashboard definition as a JSON-serializable
// map. Callers serialize it; the metric package stays free of HTTP concerns.
func Dashboard() map[string]any {
	panels := []any{
		// ── Business (the numbers this service actually exists to track) ──
		statPanel(1, 0, "Pastes Created (total)",
			"short",
			target("cairnbin_pastes_created_total", "created")),
		statPanel(2, 0, "Pastes Viewed (total)",
			"short",
			target("cairnbin_pastes_viewed_total", "viewed")),
		statPanel(3, 0, "Pastes Deleted (total)",
			"short",
			target("cairnbin_pastes_deleted_total", "deleted")),
		panel(4, 1, "Paste Activity Rate",
			"reqps",
			target("rate(cairnbin_pastes_created_total[5m])", "created/s"),
			target("rate(cairnbin_pastes_viewed_total[5m])", "viewed/s"),
			target("rate(cairnbin_pastes_deleted_total[5m])", "deleted/s"),
		),

		// ── HTTP ──
		rowPanel(10, 2, "HTTP"),
		panel(11, 3, "Request Rate by Status",
			"reqps",
			target("sum by (status) (rate(cairnbin_http_requests_total[5m]))", "{{status}}"),
			target("sum by (method) (rate(cairnbin_http_requests_total[5m]))", "method={{method}}"),
		),
		panel(12, 4, "Request Duration (p50 / p95 / p99)",
			"s",
			target(`histogram_quantile(0.50, sum by (le) (rate(cairnbin_http_request_duration_seconds_bucket[5m])))`, "p50"),
			target(`histogram_quantile(0.95, sum by (le) (rate(cairnbin_http_request_duration_seconds_bucket[5m])))`, "p95"),
			target(`histogram_quantile(0.99, sum by (le) (rate(cairnbin_http_request_duration_seconds_bucket[5m])))`, "p99"),
		),
		panel(13, 5, "Active Requests",
			"short",
			target("cairnbin_http_active_requests", "active"),
		),
		panel(14, 6, "Request / Response Size (p95)",
			"bytes",
			target(`histogram_quantile(0.95, sum by (le) (rate(cairnbin_http_request_size_bytes_bucket[5m])))`, "req p95"),
			target(`histogram_quantile(0.95, sum by (le) (rate(cairnbin_http_response_size_bytes_bucket[5m])))`, "resp p95"),
		),

		// ── Database ──
		rowPanel(20, 7, "Database"),
		panel(21, 8, "Query Rate by Table",
			"ops",
			target("sum by (table) (rate(cairnbin_db_queries_total[5m]))", "{{table}}"),
		),
		panel(22, 9, "Query Duration (p95)",
			"s",
			target(`histogram_quantile(0.95, sum by (le, table) (rate(cairnbin_db_query_duration_seconds_bucket[5m])))`, "p95 {{table}}"),
		),
		panel(23, 10, "Connection Pool (open / in use)",
			"short",
			target("cairnbin_db_connections_open", "open"),
			target("cairnbin_db_connections_in_use", "in use"),
		),
		panel(24, 11, "Database Errors",
			"reqps",
			target("sum by (error_type) (rate(cairnbin_db_errors_total[5m]))", "{{error_type}}"),
		),

		// ── Cache ──
		rowPanel(30, 12, "Cache"),
		panel(31, 13, "Cache Hit Ratio",
			"percentunit",
			target(`sum(rate(cairnbin_cache_hits_total[5m])) / clamp_min(sum(rate(cairnbin_cache_hits_total[5m])) + sum(rate(cairnbin_cache_misses_total[5m])), 1e-9)`, "hit ratio"),
		),
		panel(32, 14, "Cache Hits / Misses / Evictions Rate",
			"ops",
			target("sum by (cache) (rate(cairnbin_cache_hits_total[5m]))", "hits {{cache}}"),
			target("sum by (cache) (rate(cairnbin_cache_misses_total[5m]))", "misses {{cache}}"),
			target("sum by (cache) (rate(cairnbin_cache_evictions_total[5m]))", "evictions {{cache}}"),
		),
		panel(33, 15, "Cache Size / Bytes",
			"short",
			target("sum by (cache) (cairnbin_cache_size)", "items {{cache}}"),
			target("sum by (cache) (cairnbin_cache_bytes)", "bytes {{cache}}"),
		),

		// ── Scheduler ──
		rowPanel(40, 16, "Scheduler"),
		panel(41, 17, "Task Executions by Status",
			"ops",
			target("sum by (task, status) (rate(cairnbin_scheduler_tasks_total[5m]))", "{{task}} {{status}}"),
		),
		panel(42, 18, "Task Duration (p95)",
			"s",
			target(`histogram_quantile(0.95, sum by (le, task) (rate(cairnbin_scheduler_task_duration_seconds_bucket[5m])))`, "p95 {{task}}"),
		),
		panel(43, 19, "Running Tasks",
			"short",
			target("sum by (task) (cairnbin_scheduler_tasks_running)", "{{task}}"),
		),
		panel(44, 20, "Last Run Timestamp",
			"dateTimeAsIso",
			target("cairnbin_scheduler_last_run_timestamp", "{{task}}"),
		),

		// ── Auth, rate limiting, and Tor ──
		rowPanel(50, 21, "Authentication &amp; Access Control"),
		panel(51, 22, "Auth Attempts by Result",
			"reqps",
			target("sum by (method, status) (rate(cairnbin_auth_attempts_total[5m]))", "{{method}} {{status}}"),
		),
		panel(52, 23, "Active Sessions / API Tokens",
			"short",
			target("cairnbin_auth_sessions_active", "sessions"),
			target("cairnbin_api_tokens_active", "api tokens"),
		),
		panel(53, 24, "Rate-Limited Requests",
			"reqps",
			target("sum by (limit) (rate(cairnbin_ratelimit_requests_total[5m]))", "limited {{limit}}"),
			target("sum by (limit) (rate(cairnbin_ratelimit_blocked_total[5m]))", "blocked {{limit}}"),
		),
		panel(54, 25, "Tor Requests",
			"reqps",
			target("cairnbin_tor_enabled", "enabled"),
			target("cairnbin_tor_running", "running"),
			target("cairnbin_tor_circuit_established", "circuit"),
			target("rate(cairnbin_tor_requests_total[5m])", "requests/s"),
		),

		// ── System and runtime ──
		rowPanel(60, 26, "System"),
		panel(61, 27, "CPU Usage",
			"percent",
			target("cairnbin_system_cpu_usage_percent", "cpu"),
		),
		panel(62, 28, "Memory Used / Total",
			"bytes",
			target("cairnbin_system_memory_used_bytes", "used"),
			target("cairnbin_system_memory_total_bytes", "total"),
			target("cairnbin_system_memory_usage_percent", "usage %"),
		),
		panel(63, 29, "Disk Used / Total",
			"bytes",
			target("cairnbin_system_disk_used_bytes", "used"),
			target("cairnbin_system_disk_total_bytes", "total"),
			target("cairnbin_system_disk_usage_percent", "usage %"),
		),
		panel(64, 30, "Go Runtime — Goroutines &amp; GC",
			"short",
			target("cairnbin_go_goroutines", "goroutines"),
			target("rate(cairnbin_go_gc_runs_total[5m])", "gc runs/s"),
			target("cairnbin_go_mem_alloc_bytes", "alloc"),
			target("cairnbin_go_mem_sys_bytes", "sys"),
		),
		panel(65, 31, "Uptime",
			"s",
			target("cairnbin_app_uptime_seconds", "uptime"),
		),
	}

	return map[string]any{
		// Wrapper so the payload imports directly as a Grafana dashboard.
		"__inputs": []any{
			map[string]any{
				"name":       "DS_PROMETHEUS",
				"label":      "Prometheus",
				"type":       "datasource",
				"pluginId":   "prometheus",
				"pluginName": "Prometheus",
			},
		},
		"__requires": []any{
			map[string]any{"type": "grafana", "id": "grafana", "name": "Grafana", "version": "10.0.0"},
			map[string]any{"type": "datasource", "id": "prometheus", "name": "Prometheus", "version": "1.0.0"},
		},
		"id":            nil,
		"uid":           grafanaDashboardUID,
		"title":         "Cairnbin Overview",
		"description":   "HTTP, database, cache, scheduler, auth, and system telemetry for this cairnbin server (AI.md PART 20).",
		"tags":          []string{"cairnbin", "auto-generated"},
		"timezone":      "browser",
		"editable":      true,
		"schemaVersion": 39,
		"version":       1,
		"refresh":       "30s",
		"time": map[string]any{
			"from": "now-6h",
			"to":   "now",
		},
		"timepicker": map[string]any{
			"refresh_intervals": []string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h"},
		},
		// The bare datasource template variable Grafana resolves __inputs against.
		"__requires_datasource": nil,
		"templating": map[string]any{
			"list": []any{
				map[string]any{
					"name":       grafanaDS,
					"label":      "Prometheus datasource",
					"type":       "datasource",
					"query":      "prometheus",
					"current":    map[string]any{"text": "default", "value": grafanaDS},
					"hide":       0,
					"refresh":    1,
					"includeAll": false,
					"multi":      false,
				},
			},
		},
		"annotations": map[string]any{
			"list": []any{
				map[string]any{
					"builtIn":    1,
					"datasource": map[string]any{"type": "grafana", "uid": "-- Grafana --"},
					"enable":     true,
					"hide":       true,
					"iconColor":  "rgba(0, 211, 255, 1)",
					"name":       "Annotations & Alerts",
					"type":       "dashboard",
				},
			},
		},
		"panels": panels,
		"links":  []any{},
	}
}
