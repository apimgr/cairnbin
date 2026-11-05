package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/database"
)

// securityIDWindow is the rotation period for the security_id token: 48 hours
// (172800 seconds). The id is recomputed deterministically from the persisted
// installation_secret, so no separate state is tracked (PART 11 → Security
// Reports — Coordinated Disclosure Pipeline).
const securityIDWindow = 172800

// securityIDForWindow computes the security_id for a specific 48h window index:
// HMAC-SHA256(installation_secret, window) hex-encoded, first 16 chars.
func (s *Server) securityIDForWindow(window int64) string {
	return securityIDFor(s.installSecret, window)
}

// securityIDFor computes the security_id for one window under secret.
func securityIDFor(secret []byte, window int64) string {
	mac := hmac.New(sha256.New, secret)
	// The window index is written as its decimal ASCII form to keep the
	// derivation stable and human-auditable.
	mac.Write([]byte(itoa64(window)))
	sum := hex.EncodeToString(mac.Sum(nil))
	return sum[:16]
}

// currentSecurityID returns the security_id for the current 48h window. Empty
// when the installation secret is unavailable (never expected after startup).
func (s *Server) currentSecurityID() string {
	if len(s.installSecret) == 0 {
		return ""
	}
	return s.securityIDForWindow(time.Now().Unix() / securityIDWindow)
}

// validSecurityID reports whether the supplied id matches the current OR the
// previous 48h window's security_id. Accepting the previous window prevents
// boundary failures for a researcher who loaded security.txt just before a
// rotation. Comparison is constant-time.
func (s *Server) validSecurityID(id string) bool {
	if id == "" || len(s.installSecret) == 0 {
		return false
	}
	now := time.Now().Unix() / securityIDWindow
	for _, w := range []int64{now, now - 1} {
		want := s.securityIDForWindow(w)
		if subtle.ConstantTimeCompare([]byte(want), []byte(id)) == 1 {
			return true
		}
		// During the 7-day grace window after `secret rotate
		// installation_secret`, ids minted under the previous secret still work.
		if len(s.prevInstallSecret) > 0 {
			prev := securityIDFor(s.prevInstallSecret, w)
			if subtle.ConstantTimeCompare([]byte(prev), []byte(id)) == 1 {
				return true
			}
		}
	}
	return false
}

// loadPreviousSecrets loads the parked pre-rotation secrets that are still
// inside their grace windows (PART 11 → Secret Rotation): installation_secret
// for 7 days, the at-rest encryption key for 30 days.
func (s *Server) loadPreviousSecrets(db database.DB) {
	fresh, ok := db.(interface {
		FreshAppSecret(key string, maxAge time.Duration) ([]byte, bool)
	})
	if !ok {
		return
	}
	if raw, ok := fresh.FreshAppSecret("installation_secret_previous", 7*24*time.Hour); ok {
		s.prevInstallSecret = raw
	}
	if raw, ok := fresh.FreshAppSecret("encryption_key_previous", 30*24*time.Hour); ok {
		if key, err := hex.DecodeString(string(raw)); err == nil && len(key) == 32 {
			s.prevEncKey = key
		}
	}
}

// openAtRest opens a value sealed under the at-rest encryption key, falling
// back to the pre-rotation key during its 30-day grace window.
func (s *Server) openAtRest(key, sealed []byte) ([]byte, error) {
	plain, err := secretbox.Open(key, sealed)
	if err == nil || len(s.prevEncKey) == 0 {
		return plain, err
	}
	if prev, perr := secretbox.Open(s.prevEncKey, sealed); perr == nil {
		return prev, nil
	}
	return nil, err
}

// itoa64 formats a signed 64-bit integer as decimal ASCII without importing
// strconv at call sites that only need this one conversion.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
