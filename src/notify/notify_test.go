package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testMeta() Meta {
	return Meta{ProjectName: "cairnbin", ProjectVersion: "1.2.3", AppURL: "https://paste.example.com"}
}

func testMsg() Message {
	return Message{
		Role:      "admin",
		Event:     "admin.backup_failed",
		Subject:   "Backup failed",
		Body:      "disk full",
		Severity:  "critical",
		Timestamp: time.Unix(1700000000, 0),
	}
}

func TestPlainMessage(t *testing.T) {
	got := plainMessage(testMsg())
	for _, want := range []string{"[CRITICAL]", "Backup failed", "disk full"} {
		if !strings.Contains(got, want) {
			t.Errorf("plainMessage missing %q: %q", want, got)
		}
	}

	noSev := plainMessage(Message{Subject: "Hi"})
	if strings.Contains(noSev, "[") {
		t.Errorf("no-severity message should not carry a bracket tag: %q", noSev)
	}
}

func TestBuildRequestTransports(t *testing.T) {
	meta := testMeta()
	msg := testMsg()

	tests := []struct {
		transport    string
		dest         string
		wantMethod   string
		wantCT       string
		urlContains  []string
		bodyContains []string
	}{
		{"telegram", "https://api.telegram.org/botX/sendMessage?chat_id=5", "POST", "",
			[]string{"chat_id=5", "text=", "CRITICAL"}, nil},
		{"discord", "https://discord.com/api/webhooks/1/x", "POST", "application/json",
			nil, []string{`"content"`, `"username":"cairnbin"`, "Backup failed"}},
		{"slack", "https://hooks.slack.com/services/a/b/c", "POST", "application/json",
			nil, []string{`"text"`, "disk full"}},
		{"mattermost", "https://mm.example.com/hooks/x", "POST", "application/json",
			nil, []string{`"text"`}},
		{"pushover", "https://api.pushover.net/1/messages.json", "POST", "application/x-www-form-urlencoded",
			nil, []string{"title=", "message=", "priority="}},
		{"gotify", "https://gotify.example.com/message?token=abc", "POST", "application/json",
			nil, []string{`"title"`, `"message"`, `"priority"`}},
		{"generic", "https://hook.example.com/in", "POST", "application/json",
			nil, []string{`"role":"admin"`, `"event":"admin.backup_failed"`, `"project_name":"cairnbin"`, `"timestamp":1700000000`}},
		{"unknownxyz", "https://hook.example.com/in", "POST", "application/json",
			nil, []string{`"role":"admin"`, `"project_version":"1.2.3"`}},
	}

	for _, tc := range tests {
		t.Run(tc.transport, func(t *testing.T) {
			br, err := buildRequest(tc.transport, msg, meta, tc.dest)
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			if br.method != tc.wantMethod {
				t.Errorf("method = %q, want %q", br.method, tc.wantMethod)
			}
			if br.contentType != tc.wantCT {
				t.Errorf("contentType = %q, want %q", br.contentType, tc.wantCT)
			}
			for _, sub := range tc.urlContains {
				if !strings.Contains(br.url, sub) {
					t.Errorf("url %q missing %q", br.url, sub)
				}
			}
			for _, sub := range tc.bodyContains {
				if !strings.Contains(string(br.body), sub) {
					t.Errorf("body %q missing %q", string(br.body), sub)
				}
			}
		})
	}
}

func TestGenericPayloadOmitsEmptyTrackingID(t *testing.T) {
	br, err := buildRequest("generic", testMsg(), testMeta(), "https://h/x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(br.body), "tracking_id") {
		t.Errorf("empty tracking_id should be omitted: %s", br.body)
	}

	msg := testMsg()
	msg.TrackingID = "trk-1"
	br, _ = buildRequest("generic", msg, testMeta(), "https://h/x")
	if !strings.Contains(string(br.body), `"tracking_id":"trk-1"`) {
		t.Errorf("tracking_id should be present: %s", br.body)
	}
}

func TestSignBody(t *testing.T) {
	secret := "s3cr3t"
	body := []byte(`{"a":1}`)
	got := signBody(secret, body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Errorf("signBody = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "sha256=") {
		t.Errorf("signature must be sha256-prefixed: %q", got)
	}
}

func TestUUIDv7(t *testing.T) {
	id, err := uuidV7()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 {
		t.Errorf("uuid length = %d, want 36: %q", len(id), id)
	}
	// Version nibble must be 7 (first char of the 3rd group).
	if id[14] != '7' {
		t.Errorf("version nibble = %c, want 7: %q", id[14], id)
	}
	// Variant nibble (first char of the 4th group) must be one of 8,9,a,b.
	if !strings.ContainsRune("89ab", rune(id[19])) {
		t.Errorf("variant nibble = %c, want 8/9/a/b: %q", id[19], id)
	}
	id2, _ := uuidV7()
	if id == id2 {
		t.Errorf("expected distinct UUIDs, got %q twice", id)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://api.telegram.org/botX/send?chat_id=5": "api.telegram.org",
		"http://h.example.com":                         "h.example.com",
		"hooks.slack.com/services/a":                   "hooks.slack.com",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserAgent(t *testing.T) {
	d := New(testMeta(), nil)
	if got := d.userAgent(); got != "cairnbin/1.2.3 (+https://paste.example.com)" {
		t.Errorf("userAgent = %q", got)
	}
	d2 := New(Meta{}, nil)
	if got := d2.userAgent(); got != "cairnbin/0.0.0" {
		t.Errorf("default userAgent = %q", got)
	}
}

func TestSendSignedHeadersRoundTrip(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
		gotHdr  http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotHdr = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := New(testMeta(), srv.Client())
	msg := testMsg()
	tgt := Target{Transport: "generic", URL: srv.URL, Secret: "topsecret"}

	status, err := d.send(context.Background(), msg, tgt, "id-123")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotHdr.Get("X-Webhook-ID") != "id-123" {
		t.Errorf("X-Webhook-ID = %q", gotHdr.Get("X-Webhook-ID"))
	}
	if gotHdr.Get("X-Webhook-Event") != "admin.backup_failed" {
		t.Errorf("X-Webhook-Event = %q", gotHdr.Get("X-Webhook-Event"))
	}
	if gotHdr.Get("X-Webhook-Timestamp") != strconv.FormatInt(msg.Timestamp.Unix(), 10) {
		t.Errorf("X-Webhook-Timestamp = %q", gotHdr.Get("X-Webhook-Timestamp"))
	}
	if gotHdr.Get("User-Agent") != "cairnbin/1.2.3 (+https://paste.example.com)" {
		t.Errorf("User-Agent = %q", gotHdr.Get("User-Agent"))
	}
	if want := signBody("topsecret", gotBody); gotHdr.Get("X-Webhook-Signature") != want {
		t.Errorf("signature = %q, want %q", gotHdr.Get("X-Webhook-Signature"), want)
	}
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["role"] != "admin" {
		t.Errorf("payload role = %v", payload["role"])
	}
}

func TestDeliverRetriesThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		ids = append(ids, r.Header.Get("X-Webhook-ID"))
		n := calls
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := New(testMeta(), srv.Client())
	// Replace the backoff sleep with an instant no-op so the retry ladder runs
	// synchronously in the test.
	d.sleep = func(context.Context, time.Duration) bool { return true }

	d.deliver(context.Background(), testMsg(), Target{Transport: "generic", URL: srv.URL, Secret: "x"})

	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Errorf("X-Webhook-ID changed across retries: %v", ids)
			break
		}
	}
}

func TestDeliverDropsAfterExhaustion(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	d := New(testMeta(), srv.Client())
	d.sleep = func(context.Context, time.Duration) bool { return true }

	d.deliver(context.Background(), testMsg(), Target{Transport: "generic", URL: srv.URL, Secret: "x"})

	mu.Lock()
	defer mu.Unlock()
	// 1 initial attempt + len(backoffSchedule) retries.
	if want := 1 + len(backoffSchedule); calls != want {
		t.Fatalf("calls = %d, want %d", calls, want)
	}
}

func TestDeliverCancelStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := New(testMeta(), srv.Client())
	// A sleep that reports cancellation stops the ladder immediately.
	d.sleep = func(context.Context, time.Duration) bool { return false }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.deliver(ctx, testMsg(), Target{Transport: "generic", URL: srv.URL, Secret: "x"})
}

// TestShutdownCancelsDispatchRetries verifies that Shutdown aborts an
// in-flight Dispatch retry ladder instead of leaving it to run to exhaustion
// in the background.
func TestShutdownCancelsDispatchRetries(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	firstCall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstCall)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := New(testMeta(), srv.Client())
	// A sleep that blocks until ctx is cancelled, then reports cancellation,
	// simulates a real backoff wait that Shutdown must be able to interrupt.
	d.sleep = func(ctx context.Context, _ time.Duration) bool {
		<-ctx.Done()
		return false
	}

	d.Dispatch(testMsg(), Target{Transport: "generic", URL: srv.URL, Secret: "x"})

	// Wait for the initial (pre-backoff) attempt to actually reach the server
	// before shutting down, so Shutdown races the blocked backoff sleep, not
	// the delivery goroutine's startup.
	select {
	case <-firstCall:
	case <-time.After(2 * time.Second):
		t.Fatal("initial delivery attempt never reached the server")
	}

	if ok := d.Shutdown(2 * time.Second); !ok {
		t.Fatal("Shutdown did not complete within timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (only the initial attempt before the blocked backoff)", calls)
	}
}

// TestShutdownIsIdempotentWithNoDispatches verifies Shutdown returns true
// immediately when nothing was ever dispatched.
func TestShutdownIsIdempotentWithNoDispatches(t *testing.T) {
	d := New(testMeta(), nil)
	if ok := d.Shutdown(time.Second); !ok {
		t.Fatal("Shutdown with no in-flight deliveries should complete immediately")
	}
}
