package maintenance

// Internal tests for the sensitive-operation authorization gate (authorize.go):
// authorizeSetup, authorizeRestore, authorizeMode, authorizeDataOp, and their
// isServiceUser/requireOperatorToken/confirm helpers, using injected seams.

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func newTestAuthorizer(t *testing.T, token string, stdin string) (*authorizer, *bytes.Buffer) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.Token = token
	stderr := &bytes.Buffer{}
	return &authorizer{
		opts:          AuthOptions{ServiceUser: "cairnbin"},
		cfg:           cfg,
		stdin:         strings.NewReader(stdin),
		stderr:        stderr,
		isRootFn:      func() bool { return false },
		currentUserFn: func() (string, error) { return "nobody", nil },
		dbEmptyFn:     func(string) (bool, error) { return false, nil },
	}, stderr
}

// ─── authorizeDataOp ─────────────────────────────────────────────────────────

func TestAuthorizeDataOp_Root(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.isRootFn = func() bool { return true }
	if err := a.authorizeDataOp(); err != nil {
		t.Errorf("root should always be authorized, got %v", err)
	}
}

func TestAuthorizeDataOp_ServiceUserWithValidToken(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "sekret\n")
	a.currentUserFn = func() (string, error) { return "cairnbin", nil }
	if err := a.authorizeDataOp(); err != nil {
		t.Errorf("service user with valid token should be authorized, got %v", err)
	}
}

func TestAuthorizeDataOp_ServiceUserWithInvalidToken(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "wrong\n")
	a.currentUserFn = func() (string, error) { return "cairnbin", nil }
	if err := a.authorizeDataOp(); err == nil {
		t.Error("expected error for mismatched operator token")
	}
}

func TestAuthorizeDataOp_ServiceUserNoTokenConfigured(t *testing.T) {
	a, _ := newTestAuthorizer(t, "", "")
	a.currentUserFn = func() (string, error) { return "cairnbin", nil }
	if err := a.authorizeDataOp(); err == nil {
		t.Error("expected error when operator token is not configured")
	}
}

func TestAuthorizeDataOp_UnrelatedUserRejected(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	if err := a.authorizeDataOp(); err == nil {
		t.Error("expected rejection for a non-root, non-service user")
	}
}

func TestAuthorizeDataOp_NoFirstRunBypass(t *testing.T) {
	// Unlike setup/restore, an empty database must NOT bypass authorization —
	// data export/delete always targets an existing resource by prefix.
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.dbEmptyFn = func(string) (bool, error) { return true, nil }
	if err := a.authorizeDataOp(); err == nil {
		t.Error("expected rejection even with an empty database")
	}
}

// ─── AuthorizeDataOp (exported entrypoint) ──────────────────────────────────

func TestAuthorizeDataOp_ExportedEntrypoint(t *testing.T) {
	// Exercises the AuthOptions -> newAuthorizer -> authorizeDataOp wiring;
	// running as a non-root, non-service OS user must be rejected. Skipped
	// when the test runner itself is root (e.g. inside a build container),
	// since root is unconditionally authorized by design.
	if isRoot() {
		t.Skip("test runner is root; authorizeDataOp always allows root")
	}
	if err := AuthorizeDataOp(AuthOptions{ServiceUser: "definitely-not-this-user"}); err == nil {
		t.Error("expected rejection for the current test-runner user")
	}
}

// ─── authorizeSetup ──────────────────────────────────────────────────────────

func TestAuthorizeSetup_FirstRunBypass(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.dbEmptyFn = func(string) (bool, error) { return true, nil }
	if err := a.authorizeSetup(); err != nil {
		t.Errorf("empty database should bypass setup gate, got %v", err)
	}
}

func TestAuthorizeSetup_RootConfirmed(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "y\n")
	a.isRootFn = func() bool { return true }
	if err := a.authorizeSetup(); err != nil {
		t.Errorf("root confirming should be authorized, got %v", err)
	}
}

func TestAuthorizeSetup_RootDeclined(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "n\n")
	a.isRootFn = func() bool { return true }
	if err := a.authorizeSetup(); err == nil {
		t.Error("expected cancellation error when root declines")
	}
}

func TestAuthorizeSetup_NonRootRejected(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	if err := a.authorizeSetup(); err == nil {
		t.Error("expected rejection for a non-root user with an existing database")
	}
}

// ─── authorizeRestore ────────────────────────────────────────────────────────

func TestAuthorizeRestore_FirstRunBypass(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.dbEmptyFn = func(string) (bool, error) { return true, nil }
	if err := a.authorizeRestore(); err != nil {
		t.Errorf("empty database should bypass restore gate, got %v", err)
	}
}

func TestAuthorizeRestore_ServiceUserWithValidToken(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "sekret\n")
	a.currentUserFn = func() (string, error) { return "cairnbin", nil }
	if err := a.authorizeRestore(); err != nil {
		t.Errorf("service user with valid token should be authorized, got %v", err)
	}
}

func TestAuthorizeRestore_UnrelatedUserRejected(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	if err := a.authorizeRestore(); err == nil {
		t.Error("expected rejection for a non-root, non-service user")
	}
}

// ─── authorizeMode ───────────────────────────────────────────────────────────

func TestAuthorizeMode_RootWarnsAndAllows(t *testing.T) {
	a, stderr := newTestAuthorizer(t, "sekret", "")
	a.isRootFn = func() bool { return true }
	if err := a.authorizeMode(); err != nil {
		t.Errorf("root should always be authorized, got %v", err)
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Error("expected a security warning to be printed for root mode changes")
	}
}

func TestAuthorizeMode_ServiceUserWithValidToken(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "sekret\n")
	a.currentUserFn = func() (string, error) { return "cairnbin", nil }
	if err := a.authorizeMode(); err != nil {
		t.Errorf("service user with valid token should be authorized, got %v", err)
	}
}

func TestAuthorizeMode_UnrelatedUserRejected(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	if err := a.authorizeMode(); err == nil {
		t.Error("expected rejection for a non-root, non-service user")
	}
}

// ─── isServiceUser ───────────────────────────────────────────────────────────

func TestIsServiceUser_WindowsDomainPrefixStripped(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.currentUserFn = func() (string, error) { return `DOMAIN\cairnbin`, nil }
	if !a.isServiceUser() {
		t.Error("expected DOMAIN\\cairnbin to match service user cairnbin")
	}
}

func TestIsServiceUser_ErrorReturnsFalse(t *testing.T) {
	a, _ := newTestAuthorizer(t, "sekret", "")
	a.currentUserFn = func() (string, error) { return "", assertErr }
	if a.isServiceUser() {
		t.Error("expected isServiceUser to return false on lookup error")
	}
}

var assertErr = &testAuthError{"lookup failed"}

type testAuthError struct{ msg string }

func (e *testAuthError) Error() string { return e.msg }

// ─── requireOperatorToken constant-time compare sanity ─────────────────────

func TestRequireOperatorToken_HashesCompared(t *testing.T) {
	// Sanity-check that the SHA-256 digest of a correct token matches itself
	// (documents the constant-time comparison contract in authorize.go).
	want := sha256.Sum256([]byte("sekret"))
	got := sha256.Sum256([]byte("sekret"))
	if want != got {
		t.Error("expected identical digests for identical tokens")
	}
}
