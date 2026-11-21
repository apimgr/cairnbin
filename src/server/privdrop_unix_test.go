//go:build !windows

package server

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestResolvePrivDropTarget(t *testing.T) {
	cur, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	wantUID, _ := strconv.Atoi(cur.Uid)

	t.Run("existing user resolves", func(t *testing.T) {
		uid, _, name, ok := resolvePrivDropTarget(cur.Username, "")
		if !ok {
			t.Fatalf("expected ok for existing user %q", cur.Username)
		}
		if uid != wantUID {
			t.Errorf("uid = %d, want %d", uid, wantUID)
		}
		if name != cur.Username {
			t.Errorf("name = %q, want %q", name, cur.Username)
		}
	})

	t.Run("missing user does not resolve", func(t *testing.T) {
		if _, _, _, ok := resolvePrivDropTarget("nonexistent_user_zzz_999", ""); ok {
			t.Error("expected ok=false for missing user")
		}
	})

	t.Run("auto falls back to default service user", func(t *testing.T) {
		// The default account may or may not exist in the test environment; either
		// way the name returned for the lookup must be the default service user.
		if _, err := user.Lookup(defaultServiceUser); err != nil {
			_, _, _, ok := resolvePrivDropTarget("{auto}", "")
			if ok {
				t.Errorf("expected ok=false when %q is absent", defaultServiceUser)
			}
			return
		}
		_, _, name, ok := resolvePrivDropTarget("", "")
		if !ok || name != defaultServiceUser {
			t.Errorf("got name=%q ok=%v, want %q true", name, ok, defaultServiceUser)
		}
	})

	t.Run("explicit group override", func(t *testing.T) {
		grp, err := user.LookupGroupId(cur.Gid)
		if err != nil {
			t.Skipf("cannot look up current group: %v", err)
		}
		wantGID, _ := strconv.Atoi(cur.Gid)
		_, gid, _, ok := resolvePrivDropTarget(cur.Username, grp.Name)
		if !ok || gid != wantGID {
			t.Errorf("got gid=%d ok=%v, want %d true", gid, ok, wantGID)
		}
	})
}

func TestChownRecursive(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Chown to the current uid/gid is a privilege-free no-op that must succeed.
	uid := os.Getuid()
	gid := os.Getgid()
	if err := chownRecursive(dir, uid, gid); err != nil {
		t.Errorf("chownRecursive: %v", err)
	}

	// A missing path is silently skipped.
	if err := chownRecursive(filepath.Join(dir, "does-not-exist"), uid, gid); err != nil {
		t.Errorf("chownRecursive(missing): %v", err)
	}
}

func TestApplyPrivilegeDropNoConfig(t *testing.T) {
	s := &Server{}
	// No privDrop configured: must be a no-op regardless of euid.
	if err := s.applyPrivilegeDrop(); err != nil {
		t.Errorf("applyPrivilegeDrop with nil config: %v", err)
	}
}

func TestApplyPrivilegeDropNoTarget(t *testing.T) {
	s := &Server{}
	// A root process must refuse to continue when the service account cannot be
	// resolved; an unprivileged process needs no drop.
	s.SetPrivilegeDrop("nonexistent_user_zzz_999", "", []string{t.TempDir(), ""})
	err := s.applyPrivilegeDrop()
	if os.Geteuid() == 0 && err == nil {
		t.Error("applyPrivilegeDrop with unresolvable target succeeded as root")
	}
	if os.Geteuid() != 0 && err != nil {
		t.Errorf("unprivileged applyPrivilegeDrop returned error: %v", err)
	}
}

func TestBindAndDropNoConfig(t *testing.T) {
	s := &Server{}
	// Unprivileged ports are bound after privilege-drop processing.
	listeners, err := s.bindPrivilegedListenersAndDrop("127.0.0.1:0")
	if err != nil {
		t.Fatalf("bindPrivilegedListenersAndDrop: %v", err)
	}
	if listeners[0] != nil {
		t.Fatal("ephemeral port was bound before post-drop startup")
	}
	ln, err := bindListenerAfterDrop("127.0.0.1:0", listeners[0])
	if err != nil {
		t.Fatalf("bindListenerAfterDrop: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if ln.Addr() == nil {
		t.Error("expected a bound listener address")
	}
}

func TestApplyPrivilegeDropRejectsRootTarget(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to resolve and reject a root service target")
	}
	s := &Server{}
	s.SetPrivilegeDrop("root", "", nil)
	if err := s.applyPrivilegeDrop(); err == nil {
		t.Error("applyPrivilegeDrop accepted root as the service target")
	}
}

func TestCurrentlyRootMatchesEUID(t *testing.T) {
	if got, want := currentlyRoot(), os.Geteuid() == 0; got != want {
		t.Errorf("currentlyRoot() = %v, want %v", got, want)
	}
}
