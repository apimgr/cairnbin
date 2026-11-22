package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func TestContactMarkupAndBareDomainRules(t *testing.T) {
	a := config.ContactAntispamConfig{MaxLinks: 2, RejectMarkup: true, BlockedLinkDomains: []string{"example.shop"}}
	for _, message := range []string{"<a href=\"https://safe.example\">link</a>", "[url=https://safe.example]link[/url]"} {
		if got := contactContentReason(a, "name", "person@example.net", "subject", message); got != "markup" {
			t.Errorf("markup %q reason = %q, want markup", message, got)
		}
	}
	if got := contactContentReason(a, "name", "person@example.net", "subject", "one.example.net two.example.net mgmarket10.shop"); got != "content" {
		t.Errorf("three bare domains reason = %q, want content", got)
	}
	if got := contactContentReason(a, "name", "person@example.net", "subject", "visit a.example.shop"); got != "content" {
		t.Errorf("blocked suffix domain reason = %q, want content", got)
	}
	if got := contactContentReason(a, "name", "person@example.net", "subject", "plain text only"); got != "" {
		t.Errorf("plain message reason = %q, want none", got)
	}
}

func TestContactFormTokenAgeAndSignature(t *testing.T) {
	s := &Server{installSecret: []byte("contact-antispam-test-secret")}
	a := config.ContactAntispamConfig{MinFillSeconds: 3, MaxAgeSeconds: 60}
	now := time.Now()
	valid := s.issueContactFormToken(now.Add(-5*time.Second), a)
	if !s.validContactFormToken(valid, now, a) {
		t.Fatal("valid form token was rejected")
	}
	if s.validContactFormToken(valid+"x", now, a) {
		t.Fatal("forged form token was accepted")
	}
	tooYoung := s.issueContactFormToken(now, a)
	if s.validContactFormToken(tooYoung, now, a) {
		t.Fatal("too-young form token was accepted")
	}
	expired := s.issueContactFormToken(now.Add(-time.Minute-time.Second), a)
	if s.validContactFormToken(expired, now, a) {
		t.Fatal("expired form token was accepted")
	}
}

func TestContactStrikeBanAfterThirdRejection(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	a := config.ContactAntispamConfig{Window: "1h", Strikes: 3, BanHours: 24}
	r := httptest.NewRequest(http.MethodPost, "/server/contact", nil)
	r.RemoteAddr = "203.0.113.19:4567"
	now := time.Now()
	for i := 0; i < 2; i++ {
		if s.recordContactBlock(a, r, "captcha", true, now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("strike %d unexpectedly banned the IP", i+1)
		}
	}
	if !s.recordContactBlock(a, r, "captcha", true, now.Add(2*time.Second)) {
		t.Fatal("third strike did not ban the IP")
	}
	if !s.contactIsBanned(r, now.Add(3*time.Second)) {
		t.Fatal("IP is not reported as banned")
	}
	if s.contactIsBanned(r, now.Add(25*time.Hour)) {
		t.Fatal("expired ban remained active")
	}
}

func TestContactRateLimitsCountAcceptedSubmissions(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	a := config.ContactAntispamConfig{Window: "1h", PerIP: 2, PerEmail: 2, Global: 10}
	r := httptest.NewRequest(http.MethodPost, "/server/contact", nil)
	r.RemoteAddr = "203.0.113.20:4567"
	now := time.Now()
	for i := 0; i < 2; i++ {
		if reason := s.allowContactSubmission(a, r, "Person@example.net", now.Add(time.Duration(i)*time.Second)); reason != "" {
			t.Fatalf("accepted submission %d rejected with %q", i+1, reason)
		}
	}
	if reason := s.allowContactSubmission(a, r, "person@example.net", now.Add(3*time.Second)); reason != "rate_ip" {
		t.Fatalf("third submission reason = %q, want rate_ip", reason)
	}
}

func TestContactHoneypotSilentlyDrops(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "simple"
	form := url.Values{
		"csrf_token": {"test"}, "form_token": {s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))},
		"website": {"filled by a bot"}, "name": {"bot"}, "email": {"bot@example.net"},
		"type": {"privacy"}, "reported_url": {""}, "reason": {""}, "copyrighted_work": {""},
		"good_faith": {"0"}, "accuracy": {"0"}, "signature": {""}, "request_kind": {"access"},
		"subject": {"message"}, "message": {"payload"},
	}
	req := httptest.NewRequest(http.MethodPost, "/server/contact", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.handleContactPost(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="contact-review-title"`) || strings.Contains(w.Body.String(), s.cfg.Server.Pages.Contact.SuccessMessage) {
		t.Fatalf("honeypot response was not the review page: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestContactThirdCaptchaFailureBansIP(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "simple"
	for i := 0; i < 3; i++ {
		form := url.Values{
			"csrf_token": {"test"}, "form_token": {s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))},
			"website": {""}, "name": {"Alice"}, "email": {fmt.Sprintf("alice%d@example.net", i)},
			"type": {"privacy"}, "reported_url": {""}, "reason": {""}, "copyrighted_work": {""},
			"good_faith": {"0"}, "accuracy": {"0"}, "signature": {""}, "request_kind": {"access"},
			"subject": {fmt.Sprintf("subject %d", i)}, "message": {fmt.Sprintf("message %d", i)},
			"captcha_token": {"invalid"}, "captcha": {"wrong"},
		}
		req := httptest.NewRequest(http.MethodPost, "/server/contact", strings.NewReader(form.Encode()))
		req.RemoteAddr = "203.0.113.21:4567"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		w := httptest.NewRecorder()
		s.handleContactPost(w, req)
		if i < 2 && w.Code != http.StatusBadRequest {
			t.Fatalf("captcha failure %d status=%d, want 400", i+1, w.Code)
		}
		if i == 2 && (w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "") {
			t.Fatalf("third captcha failure status=%d retry-after=%q, want 429 with retry-after", w.Code, w.Header().Get("Retry-After"))
		}
	}
}

func TestAcceptedContactSubmissionSendsOneConfiguredSenderEmail(t *testing.T) {
	s := newSecurityReportTestServer(t)
	host, port, attempts, messages := startSecurityReportSMTP(t, "")
	configureSecurityReportSMTP(s, host, port)
	s.cfg.Server.Contact.General.Email = "maintainer@example.test"
	s.cfg.Server.Pages.Contact.Antispam.ReviewMinSeconds = 0
	challenge, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatal(err)
	}
	var left, right int
	if _, err := fmt.Sscanf(challenge.Question, "What is %d + %d?", &left, &right); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token": {"test"}, "form_token": {s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))},
		"website": {""}, "name": {"Alice Example"}, "email": {"alice@example.net"},
		"type": {"privacy"}, "reported_url": {""}, "reason": {""}, "copyrighted_work": {""},
		"good_faith": {"0"}, "accuracy": {"0"}, "signature": {""}, "request_kind": {"access"},
		"subject": {"Question"}, "message": {"Please contact me about the service."},
		"captcha_token": {challenge.Token}, "captcha": {fmt.Sprint(left + right)},
	}
	req := httptest.NewRequest(http.MethodPost, "/server/contact", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	s.handleContactPost(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="contact-review-title"`) {
		t.Fatalf("contact submit did not produce review: status=%d body=%s", w.Code, w.Body.String())
	}
	value := func(field string) string {
		pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(field) + `" value="([^"]*)"`)
		match := pattern.FindStringSubmatch(w.Body.String())
		if len(match) != 2 {
			t.Fatalf("review response missing %s", field)
		}
		return match[1]
	}
	form.Set("form_token", value("form_token"))
	form.Set("review_token", value("review_token"))
	form.Set("step", "confirm")
	form.Set("confirmed", "1")
	form.Del("captcha_token")
	form.Del("captcha")
	confirm := httptest.NewRequest(http.MethodPost, "/server/contact", strings.NewReader(form.Encode()))
	confirm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	confirm.Header.Set("Origin", "http://example.com")
	w = httptest.NewRecorder()
	s.handleContactPost(w, confirm)
	if w.Code != http.StatusOK {
		t.Fatalf("contact confirmation status=%d body=%s", w.Code, w.Body.String())
	}
	message := <-messages
	if message.recipient != "maintainer@example.test" {
		t.Errorf("contact email recipient = %q", message.recipient)
	}
	if !strings.Contains(message.body, `From: "Cairnbin Security" <sender@example.test>`) {
		t.Errorf("contact email did not use configured sender: %s", message.body)
	}
	assertSMTPAttempts(t, attempts, 1)
}
