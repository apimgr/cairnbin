package server

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// captchaTTL bounds how long a built-in simple captcha challenge stays valid.
const captchaTTL = 10 * time.Minute

// simpleCaptcha is a rendered arithmetic challenge: a human-readable question
// plus a signed token that encodes the expected answer and an expiry. The token
// is stateless (HMAC-signed with the server csrfSecret) so no server-side
// session store is required to validate the answer on submission (PART 31).
type simpleCaptcha struct {
	Question string
	Token    string
}

type simpleCaptchaState struct {
	answerHash [32]byte
	expires    time.Time
}

// generateSimpleCaptcha builds an addition challenge with two single-digit
// operands and returns the question and a signed answer token.
func (s *Server) generateSimpleCaptcha() (simpleCaptcha, error) {
	buf := make([]byte, 2)
	if _, err := crand.Read(buf); err != nil {
		return simpleCaptcha{}, err
	}
	a := int(buf[0]%9) + 1
	b := int(buf[1]%9) + 1
	answer := strconv.Itoa(a + b)
	var id [32]byte
	if _, err := crand.Read(id[:]); err != nil {
		return simpleCaptcha{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(id[:])
	s.captchaMu.Lock()
	if s.captchaChallenges == nil {
		s.captchaChallenges = make(map[string]simpleCaptchaState)
	}
	now := time.Now()
	for key, challenge := range s.captchaChallenges {
		if !challenge.expires.After(now) {
			delete(s.captchaChallenges, key)
		}
	}
	s.captchaChallenges[token] = simpleCaptchaState{answerHash: sha256.Sum256([]byte(answer)), expires: now.Add(captchaTTL)}
	s.captchaMu.Unlock()
	return simpleCaptcha{
		Question: fmt.Sprintf("What is %d + %d?", a, b),
		Token:    token,
	}, nil
}

// validateSimpleCaptcha reports whether the submitted answer matches the signed
// token and the token has not expired. Comparison is constant-time.
func (s *Server) validateSimpleCaptcha(token, answer string) bool {
	if _, err := base64.RawURLEncoding.DecodeString(token); err != nil {
		return false
	}
	s.captchaMu.Lock()
	challenge, ok := s.captchaChallenges[token]
	if ok {
		delete(s.captchaChallenges, token)
	}
	s.captchaMu.Unlock()
	if !ok || !challenge.expires.After(time.Now()) || strings.TrimSpace(answer) == "" {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(answer)))
	return subtle.ConstantTimeCompare(challenge.answerHash[:], got[:]) == 1
}

// signCaptchaPayload signs an opaque captcha payload with the server csrfSecret
// and returns "base64(payload).base64(hmac)".
func (s *Server) signCaptchaPayload(payload string) string {
	mac := hmac.New(sha256.New, s.csrfSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) contactCaptchaProvider(r *http.Request) string {
	provider := s.liveCfg().Server.Pages.Contact.Captcha
	if s.isOverlayRequest(r) {
		return "simple"
	}
	return provider
}

func (s *Server) verifyContactCaptcha(r *http.Request, token, answer, response string) bool {
	provider := s.contactCaptchaProvider(r)
	if provider == "simple" {
		return s.validateSimpleCaptcha(token, answer)
	}
	pc := s.liveCfg().Server.Pages.Contact
	endpoint := ""
	form := url.Values{"secret": {pc.CaptchaSecretKey}, "response": {response}}
	switch provider {
	case "recaptcha":
		endpoint = "https://www.google.com/recaptcha/api/siteverify"
		if ip := clientIP(r); ip != "" {
			form.Set("remoteip", ip)
		}
	case "hcaptcha":
		endpoint = "https://api.hcaptcha.com/siteverify"
	default:
		return false
	}
	client := s.outboundHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Success bool `json:"success"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil {
		return false
	}
	return result.Success
}
