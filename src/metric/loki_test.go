package metric

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lokiCollector builds a collector with distinct per-service tokens and a fixed
// log provider, so the loki stream shape can be asserted without a server.
func lokiCollector(t *testing.T, maxEntries int, maxAge string, entries []LokiEntry) *Collector {
	t.Helper()
	return NewWithOptions(Options{
		Version:        "1.0.0",
		Commit:         "abc",
		BuildDate:      "2024-01-01",
		StartTime:      time.Now(),
		IncludeRuntime: true,
		ServiceTokens: map[string]string{
			ServicePrometheus: "prom-token",
			ServiceGrafana:    "graf-token",
			ServiceLoki:       "loki-token",
		},
		LokiMaxEntries: maxEntries,
		LokiMaxAge:     maxAge,
		LogProvider:    func() []LokiEntry { return entries },
	})
}

// getService issues an authorized GET against a metrics service handler.
func getService(t *testing.T, c *Collector, service, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/server/metrics/"+service, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c.ServiceHandler(service).ServeHTTP(rec, req)
	return rec
}

// decodeLoki parses a loki push-API response body.
func decodeLoki(t *testing.T, body []byte) []struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
} {
	t.Helper()
	var got struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("loki response is not valid JSON: %v", err)
	}
	return got.Streams
}

// TestServiceHandler_Grafana verifies the grafana service returns JSON, not the
// Prometheus text exposition. Before the per-service split, grafana fell through
// to the promhttp handler and served scrape text.
func TestServiceHandler_Grafana(t *testing.T) {
	c := lokiCollector(t, 1000, "1h", nil)
	rec := getService(t, c, ServiceGrafana, "graf-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rec.Body.String()
	// Prometheus text exposition always carries these HELP/TYPE comments; their
	// absence proves the grafana branch ran instead of the promhttp handler.
	if strings.Contains(body, "# HELP") || strings.Contains(body, "# TYPE") {
		t.Errorf("grafana served Prometheus text exposition, not the dashboard JSON")
	}
	var dash map[string]any
	if err := json.Unmarshal([]byte(body), &dash); err != nil {
		t.Fatalf("grafana response is not valid JSON: %v", err)
	}
	for _, key := range []string{"uid", "title", "panels", "schemaVersion"} {
		if _, ok := dash[key]; !ok {
			t.Errorf("dashboard is missing required key %q", key)
		}
	}
}

// TestServiceHandler_Loki verifies the loki service returns the Loki push-API
// stream shape rather than Prometheus text.
func TestServiceHandler_Loki(t *testing.T) {
	now := time.Now()
	c := lokiCollector(t, 1000, "1h", []LokiEntry{
		{Labels: map[string]string{"file": "server.log", "level": "info"}, Time: now, Line: "server started"},
	})
	rec := getService(t, c, ServiceLoki, "loki-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "# HELP") || strings.Contains(body, "# TYPE") {
		t.Errorf("loki served Prometheus text exposition, not the stream JSON")
	}

	streams := decodeLoki(t, rec.Body.Bytes())
	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	if streams[0].Stream["file"] != "server.log" {
		t.Errorf("file label = %q, want server.log", streams[0].Stream["file"])
	}
	if len(streams[0].Values) != 1 {
		t.Fatalf("values = %d, want 1", len(streams[0].Values))
	}
	// Loki requires [timestamp, line] pairs, with the timestamp in nanoseconds.
	ts, line := streams[0].Values[0][0], streams[0].Values[0][1]
	if line != "server started" {
		t.Errorf("line = %q, want %q", line, "server started")
	}
	parsed, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q is not an integer: %v", ts, err)
	}
	if delta := time.Unix(0, parsed).Sub(now); delta > time.Minute || delta < -time.Minute {
		t.Errorf("timestamp %q is %v from the entry time", ts, delta)
	}
}

// TestServiceHandler_Loki_GroupsByLabels verifies entries sharing a label set
// collapse into one stream, and differing label sets stay separate.
func TestServiceHandler_Loki_GroupsByLabels(t *testing.T) {
	now := time.Now()
	c := lokiCollector(t, 1000, "1h", []LokiEntry{
		{Labels: map[string]string{"file": "server.log"}, Time: now, Line: "one"},
		{Labels: map[string]string{"file": "error.log"}, Time: now, Line: "two"},
		{Labels: map[string]string{"file": "server.log"}, Time: now, Line: "three"},
	})
	streams := decodeLoki(t, getService(t, c, ServiceLoki, "loki-token").Body.Bytes())

	if len(streams) != 2 {
		t.Fatalf("streams = %d, want 2 (one per distinct label set)", len(streams))
	}
	total := 0
	for _, s := range streams {
		total += len(s.Values)
		if s.Stream["file"] == "server.log" && len(s.Values) != 2 {
			t.Errorf("server.log stream has %d values, want 2", len(s.Values))
		}
	}
	if total != 3 {
		t.Errorf("total values = %d, want 3", total)
	}
}

// TestServiceHandler_Loki_MaxEntries verifies the newest entries are kept when
// the provider returns more than loki.max_entries (oldest-first input).
func TestServiceHandler_Loki_MaxEntries(t *testing.T) {
	now := time.Now()
	entries := make([]LokiEntry, 0, 10)
	for i := 0; i < 10; i++ {
		entries = append(entries, LokiEntry{
			Labels: map[string]string{"file": "server.log"},
			Time:   now.Add(time.Duration(i) * time.Second),
			Line:   string(rune('a' + i)),
		})
	}
	streams := decodeLoki(t, getService(t, lokiCollector(t, 3, "1h", entries), ServiceLoki, "loki-token").Body.Bytes())

	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	// Oldest-first input, so the newest three are 'h', 'i', 'j'.
	want := []string{"h", "i", "j"}
	if len(streams[0].Values) != len(want) {
		t.Fatalf("values = %d, want %d", len(streams[0].Values), len(want))
	}
	for i, v := range streams[0].Values {
		if v[1] != want[i] {
			t.Errorf("value[%d] = %q, want %q (newest entries kept)", i, v[1], want[i])
		}
	}
}

func TestServiceHandler_Loki_MaxEntriesSortsGroupedProviderEntries(t *testing.T) {
	now := time.Now()
	entries := []LokiEntry{
		{Labels: map[string]string{"file": "access.log"}, Time: now.Add(5 * time.Second), Line: "newest"},
		{Labels: map[string]string{"file": "server.log"}, Time: now.Add(time.Second), Line: "oldest"},
		{Labels: map[string]string{"file": "access.log"}, Time: now.Add(3 * time.Second), Line: "middle"},
	}
	streams := decodeLoki(t, getService(t, lokiCollector(t, 2, "1h", entries), ServiceLoki, "loki-token").Body.Bytes())
	var lines []string
	for _, stream := range streams {
		for _, value := range stream.Values {
			lines = append(lines, value[1])
		}
	}
	if len(lines) != 2 || lines[0] != "middle" || lines[1] != "newest" {
		t.Errorf("Loki retained entries = %v, want [middle newest] in timestamp order", lines)
	}
}

// TestServiceHandler_Loki_MaxAge verifies entries older than loki.max_age are
// dropped, and a zero timestamp is kept — an unknown age is not a reason to
// discard a line.
func TestServiceHandler_Loki_MaxAge(t *testing.T) {
	now := time.Now()
	c := lokiCollector(t, 1000, "1h", []LokiEntry{
		{Labels: map[string]string{"file": "server.log"}, Time: now.Add(-2 * time.Hour), Line: "stale"},
		{Labels: map[string]string{"file": "server.log"}, Time: now.Add(-time.Minute), Line: "fresh"},
		{Labels: map[string]string{"file": "server.log"}, Time: time.Time{}, Line: "no-timestamp"},
	})
	streams := decodeLoki(t, getService(t, c, ServiceLoki, "loki-token").Body.Bytes())

	var lines []string
	for _, s := range streams {
		for _, v := range s.Values {
			lines = append(lines, v[1])
		}
	}
	if len(lines) != 2 {
		t.Fatalf("values = %v, want 2 (the 2h-old entry dropped)", lines)
	}
	for _, l := range lines {
		if l == "stale" {
			t.Errorf("entry older than max_age was not dropped")
		}
	}
}

// TestServiceHandler_Loki_NoProvider verifies a nil provider yields an empty
// stream set rather than an error or a panic.
func TestServiceHandler_Loki_NoProvider(t *testing.T) {
	c := NewWithOptions(Options{
		Version: "1.0.0",
		ServiceTokens: map[string]string{
			ServiceLoki: "loki-token",
		},
		LokiMaxEntries: 1000,
		LokiMaxAge:     "1h",
		// LogProvider deliberately nil.
	})
	rec := getService(t, c, ServiceLoki, "loki-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if streams := decodeLoki(t, rec.Body.Bytes()); len(streams) != 0 {
		t.Errorf("streams = %v, want empty", streams)
	}
}

// TestServiceHandler_ServiceTokensAreSeparate verifies each service enforces
// its own token: a grafana token must not authorize loki.
func TestServiceHandler_ServiceTokensAreSeparate(t *testing.T) {
	c := lokiCollector(t, 1000, "1h", nil)

	if rec := getService(t, c, ServiceLoki, "graf-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("loki with grafana token: status = %d, want 401", rec.Code)
	}
	if rec := getService(t, c, ServiceGrafana, "prom-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("grafana with prometheus token: status = %d, want 401", rec.Code)
	}
	if rec := getService(t, c, ServicePrometheus, "prom-token"); rec.Code != http.StatusOK {
		t.Errorf("prometheus with its own token: status = %d, want 200", rec.Code)
	}
}

// TestServiceHandler_DisabledService verifies a service with no token answers
// 403 with an empty body, so a disabled service leaks nothing.
func TestServiceHandler_DisabledService(t *testing.T) {
	c := NewWithOptions(Options{
		Version: "1.0.0",
		ServiceTokens: map[string]string{
			ServicePrometheus: "prom-token",
			// grafana and loki have no token: administratively off.
		},
		LokiMaxEntries: 1000,
		LokiMaxAge:     "1h",
	})

	for _, service := range []string{ServiceGrafana, ServiceLoki} {
		rec := getService(t, c, service, "anything")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", service, rec.Code)
		}
		if body := rec.Body.String(); body != "" {
			t.Errorf("%s: disabled service returned a body: %q", service, body)
		}
	}
}

// TestServiceHandler_UnauthorizedIsJSON verifies the rejection uses the
// canonical PART 14 envelope, not bare text.
func TestServiceHandler_UnauthorizedIsJSON(t *testing.T) {
	rec := getService(t, lokiCollector(t, 1000, "1h", nil), ServiceLoki, "wrong-token")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("401 body is not valid JSON: %v", err)
	}
	if got.OK || got.Error == "" {
		t.Errorf("401 envelope = %+v, want ok=false and a non-empty error code", got)
	}
}
