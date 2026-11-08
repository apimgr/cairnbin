package service

import "testing"

func TestServiceInstallMessagesUseConfiguredLanguage(t *testing.T) {
	SetOutputLanguage("es")
	t.Cleanup(func() { SetOutputLanguage("en") })
	if got := outputText("uninstall_cancelled"); got != "Desinstalación cancelada." {
		t.Fatalf("Spanish uninstall message = %q", got)
	}
	if got := outputText("service_installed_at", "path", "/tmp/cairnbin.service"); got != "Servicio instalado en: /tmp/cairnbin.service" {
		t.Fatalf("Spanish install message = %q", got)
	}
}
