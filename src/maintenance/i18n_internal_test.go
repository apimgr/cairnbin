package maintenance

import (
	"strings"
	"testing"
)

func TestMaintenanceTextUsesConfiguredLanguage(t *testing.T) {
	SetOutputLanguage("es")
	t.Cleanup(func() { SetOutputLanguage("en") })
	got := maintenanceText("pgp_public_written", "path", "/tmp/key.asc")
	if !strings.Contains(got, "Clave pública") || !strings.Contains(got, "/tmp/key.asc") {
		t.Fatalf("localized PGP output = %q", got)
	}
}

func TestSecretRotationMessagesUseConfiguredLanguage(t *testing.T) {
	SetOutputLanguage("es")
	t.Cleanup(func() { SetOutputLanguage("en") })
	got := maintenanceText("secret_install_rotated", "count", 2)
	if !strings.Contains(got, "rotado") || !strings.Contains(got, "2") {
		t.Fatalf("localized secret rotation output = %q", got)
	}
}

func TestComplianceAndDataMessagesUseConfiguredLanguage(t *testing.T) {
	SetOutputLanguage("es")
	t.Cleanup(func() { SetOutputLanguage("en") })
	if got := maintenanceText("compliance_heading"); !strings.Contains(got, "cumplimiento") {
		t.Fatalf("localized compliance heading = %q", got)
	}
	if got := maintenanceText("data_deleted", "prefix", "tok_abc"); !strings.Contains(got, "Se eliminó") || !strings.Contains(got, "tok_abc") {
		t.Fatalf("localized data deletion output = %q", got)
	}
}
