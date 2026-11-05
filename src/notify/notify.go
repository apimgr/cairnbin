// Package notify implements the outbound webhook subsystem (PART 17). It turns
// a role-scoped Message into transport-specific HTTP requests (Telegram,
// Discord, Slack, Mattermost, Pushover, Gotify, generic), signs every request
// with an HMAC-SHA256 origin signature, and retries non-2xx deliveries with
// exponential backoff while reusing a stable idempotency key.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is a single logical notification. Role/Event/Severity classify it;
// Subject/Body carry the human-readable content. TrackingID is optional and
// only surfaces in the generic transport payload.
type Message struct {
	Role       string
	Event      string
	Subject    string
	Body       string
	Severity   string
	TrackingID string
	Timestamp  time.Time
}

// Target is a resolved webhook destination: the transport adapter name, the
// destination URL, and the per-webhook secret used to sign the body.
type Target struct {
	Transport string
	URL       string
	Secret    string
}

// Meta carries build/identity values embedded in every request (User-Agent and
// the generic payload).
type Meta struct {
	ProjectName    string
	ProjectVersion string
	AppURL         string
}

// backoffSchedule is the fixed retry ladder from PART 17: on non-2xx the
// delivery is retried after each delay in turn, then dropped.
var backoffSchedule = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

// failureLogWindow rate-limits the notify.webhook_failed log line per host so a
// permanently broken receiver cannot flood the log.
const failureLogWindow = 10 * time.Minute

// Dispatcher builds, signs, and delivers webhook requests. It is safe for
// concurrent use.
type Dispatcher struct {
	meta   Meta
	client *http.Client
	sleep  func(context.Context, time.Duration) bool

	// ctx is cancelled by Shutdown so in-flight retry ladders (which can span
	// up to 24h per PART 17) abort promptly instead of outliving the server
	// process indefinitely.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	lastFail map[string]time.Time
}

// New returns a Dispatcher with a bounded HTTP client. A nil client uses a
// default with a 15-second timeout.
func New(meta Meta, client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		meta:     meta,
		client:   client,
		sleep:    sleepCtx,
		ctx:      ctx,
		cancel:   cancel,
		lastFail: make(map[string]time.Time),
	}
}

// Dispatch delivers msg to every target in the background, retrying each
// independently. It returns immediately; delivery outcomes are logged.
func (d *Dispatcher) Dispatch(msg Message, targets ...Target) {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	for _, tgt := range targets {
		if strings.TrimSpace(tgt.URL) == "" {
			continue
		}
		d.wg.Add(1)
		go func(tgt Target) {
			defer d.wg.Done()
			d.deliver(d.ctx, msg, tgt)
		}(tgt)
	}
}

// Shutdown cancels every in-flight delivery's context and waits (up to
// timeout) for their goroutines to exit, so a server shutdown doesn't leak
// retry-ladder goroutines. It returns false if timeout elapsed first.
func (d *Dispatcher) Shutdown(timeout time.Duration) bool {
	d.cancel()
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// deliver runs the full retry ladder for a single target, reusing one
// idempotency key across every attempt.
func (d *Dispatcher) deliver(ctx context.Context, msg Message, tgt Target) {
	id, err := uuidV7()
	if err != nil {
		log.Printf("notify.webhook_failed: id generation failed for %s: %v", tgt.Transport, err)
		return
	}
	attempt := 0
	for {
		status, err := d.send(ctx, msg, tgt, id)
		if err == nil && status >= 200 && status < 300 {
			return
		}
		if attempt >= len(backoffSchedule) {
			d.logFailure(tgt, id, status, err)
			return
		}
		if !d.sleep(ctx, backoffSchedule[attempt]) {
			return
		}
		attempt++
	}
}

// send builds, signs, and performs a single delivery attempt, returning the
// HTTP status code (0 on transport error).
func (d *Dispatcher) send(ctx context.Context, msg Message, tgt Target, id string) (int, error) {
	req, err := d.buildSigned(ctx, msg, tgt, id)
	if err != nil {
		return 0, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// buildSigned constructs the transport request and attaches the five signing
// headers (X-Webhook-Signature/Timestamp/ID/Event + User-Agent).
func (d *Dispatcher) buildSigned(ctx context.Context, msg Message, tgt Target, id string) (*http.Request, error) {
	built, err := buildRequest(tgt.Transport, msg, d.meta, tgt.URL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, built.method, built.url, bytes.NewReader(built.body))
	if err != nil {
		return nil, err
	}
	if built.contentType != "" {
		req.Header.Set("Content-Type", built.contentType)
	}
	req.Header.Set("X-Webhook-Signature", signBody(tgt.Secret, built.body))
	req.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(msg.Timestamp.Unix(), 10))
	req.Header.Set("X-Webhook-ID", id)
	req.Header.Set("X-Webhook-Event", msg.Event)
	req.Header.Set("User-Agent", d.userAgent())
	return req, nil
}

// userAgent renders the PART 17 User-Agent string.
func (d *Dispatcher) userAgent() string {
	name := d.meta.ProjectName
	if name == "" {
		name = "cairnbin"
	}
	ver := d.meta.ProjectVersion
	if ver == "" {
		ver = "0.0.0"
	}
	ua := name + "/" + ver
	if d.meta.AppURL != "" {
		ua += " (+" + d.meta.AppURL + ")"
	}
	return ua
}

// logFailure emits notify.webhook_failed at most once per failureLogWindow per
// destination host.
func (d *Dispatcher) logFailure(tgt Target, id string, status int, err error) {
	host := hostOf(tgt.URL)
	d.mu.Lock()
	last, ok := d.lastFail[host]
	now := time.Now()
	if ok && now.Sub(last) < failureLogWindow {
		d.mu.Unlock()
		return
	}
	d.lastFail[host] = now
	d.mu.Unlock()
	if err != nil {
		log.Printf("notify.webhook_failed: transport=%s host=%s id=%s err=%v", tgt.Transport, host, id, err)
		return
	}
	log.Printf("notify.webhook_failed: transport=%s host=%s id=%s status=%d", tgt.Transport, host, id, status)
}

// signBody returns the X-Webhook-Signature value: sha256=<hex HMAC-SHA256>.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// hostOf extracts the host portion of a URL for failure-log rate-limiting,
// without leaking query-string secrets.
func hostOf(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// sleepCtx sleeps for d unless ctx is cancelled first; it reports whether the
// full delay elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
