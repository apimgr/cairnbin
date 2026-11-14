package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func newCaptchaTestServer() *Server {
	return &Server{csrfSecret: []byte("captcha-unit-test-secret-key")}
}

// answerFromQuestion parses "What is A + B?" and returns the expected sum.
func answerFromQuestion(t *testing.T, q string) string {
	t.Helper()
	trimmed := strings.TrimSuffix(strings.TrimPrefix(q, "What is "), "?")
	parts := strings.Split(trimmed, " + ")
	if len(parts) != 2 {
		t.Fatalf("unexpected captcha question format: %q", q)
	}
	a, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		t.Fatalf("bad operand: %v", err)
	}
	b, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		t.Fatalf("bad operand: %v", err)
	}
	return strconv.Itoa(a + b)
}

func TestSimpleCaptchaRoundTrip(t *testing.T) {
	s := newCaptchaTestServer()
	cap, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatalf("generateSimpleCaptcha: %v", err)
	}
	if cap.Question == "" || cap.Token == "" {
		t.Fatal("expected non-empty question and token")
	}
	answer := answerFromQuestion(t, cap.Question)
	if !s.validateSimpleCaptcha(cap.Token, answer) {
		t.Errorf("valid answer %q rejected for question %q", answer, cap.Question)
	}
	if s.validateSimpleCaptcha(cap.Token, answer) {
		t.Error("single-use challenge was accepted more than once")
	}
}

func TestSimpleCaptchaWrongAnswer(t *testing.T) {
	s := newCaptchaTestServer()
	cap, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatalf("generateSimpleCaptcha: %v", err)
	}
	correct := answerFromQuestion(t, cap.Question)
	wrong := correct + "9"
	if s.validateSimpleCaptcha(cap.Token, wrong) {
		t.Errorf("wrong answer %q accepted", wrong)
	}
}

func TestSimpleCaptchaRejectsEmptyAndMalformed(t *testing.T) {
	s := newCaptchaTestServer()
	cap, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatalf("generateSimpleCaptcha: %v", err)
	}
	answer := answerFromQuestion(t, cap.Question)
	cases := []struct {
		name   string
		token  string
		answer string
	}{
		{"empty answer", cap.Token, ""},
		{"empty token", "", answer},
		{"no separator", "notoken", answer},
		{"bad base64", "!!!.###", answer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s.validateSimpleCaptcha(tc.token, tc.answer) {
				t.Errorf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestSimpleCaptchaTamperedSignatureRejected(t *testing.T) {
	s := newCaptchaTestServer()
	cap, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatalf("generateSimpleCaptcha: %v", err)
	}
	answer := answerFromQuestion(t, cap.Question)
	// A token signed with a different secret must not validate.
	other := &Server{csrfSecret: []byte("a-completely-different-secret")}
	if other.validateSimpleCaptcha(cap.Token, answer) {
		t.Error("token signed with foreign secret was accepted")
	}
}

func TestSimpleCaptchaExpired(t *testing.T) {
	s := newCaptchaTestServer()
	// Forge an already-expired payload signed with the real secret.
	expired := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	payload := "5:" + expired
	token := s.signCaptchaPayload(payload)
	if s.validateSimpleCaptcha(token, "5") {
		t.Error("expired captcha token was accepted")
	}
}
