package email

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

func TestValidateTemplate(t *testing.T) {
	cases := []struct {
		name, text, wantErr string
	}{
		{"valid", "Subject: Hi {app_name}\n---\nBody {fqdn}\n", ""},
		{"unknown var with hint", "Subject: Hi\n---\nBody {fqdm}\n", "Unknown variable: {fqdm}. Did you mean {fqdn}?"},
		{"unknown var no hint", "Subject: Hi\n---\nBody {zzzzzzzzzz}\n", "Unknown variable: {zzzzzzzzzz}"},
		{"empty subject", "Subject:\n---\nBody\n", "Subject cannot be empty"},
		{"empty body", "Subject: Hi\n---\n  \n", "Body cannot be empty"},
		{"no subject line", "Hello\n---\nBody\n", "Invalid template syntax at line 1"},
		{"no separator", "Subject: Hi\nBody\n", "Invalid template syntax"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs, _ := ValidateTemplate(c.text)
			joined := strings.Join(errs, "|")
			if c.wantErr == "" && len(errs) > 0 {
				t.Fatalf("unexpected errors: %s", joined)
			}
			if c.wantErr != "" && !strings.Contains(joined, c.wantErr) {
				t.Fatalf("errors %q missing %q", joined, c.wantErr)
			}
		})
	}
}

func TestValidateTemplateLongSubjectWarns(t *testing.T) {
	errs, warns := ValidateTemplate("Subject: " + strings.Repeat("x", 90) + "\n---\nBody\n")
	if len(errs) != 0 || len(warns) != 1 {
		t.Fatalf("errs=%v warns=%v, want no errors and one warning", errs, warns)
	}
}

func TestEmbeddedTemplatesValidate(t *testing.T) {
	entries, _ := defaultTemplatesFS.ReadDir("templates")
	for _, e := range entries {
		data, _ := defaultTemplatesFS.ReadFile("templates/" + e.Name())
		if errs, _ := ValidateTemplate(string(data)); len(errs) > 0 {
			t.Errorf("%s: %v", e.Name(), errs)
		}
	}
}

func TestLoadTemplateInvalidCustomFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("Subject: x\n---\n{bogus_var}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(&config.EmailConfig{TemplateDir: dir}, "App", "https://example.com", "example.com")
	got, err := m.loadTemplate("test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "bogus_var") {
		t.Error("invalid custom template must be ignored in favor of the default")
	}
}

func TestEmbeddedEmailTemplatesHaveEveryLocale(t *testing.T) {
	entries, err := defaultTemplatesFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".txt")
		for _, lang := range []string{"en", "es", "zh", "fr", "ar", "de", "it", "ja"} {
			for _, field := range []string{"subject", "body"} {
				key := "email.templates." + name + "." + field
				if got := i18n.Translate(lang, key); got == key {
					t.Errorf("%s is missing for locale %s", key, lang)
				}
			}
		}
	}
}

func TestRenderTemplateUsesConfiguredLocale(t *testing.T) {
	m := New(&config.EmailConfig{}, "Cairnbin", "https://example.com", "example.com", "es")
	rendered, err := m.renderTemplate("backup_complete", map[string]string{
		"filename": "backup.tar.gz",
		"size":     "12 MB",
	})
	if err != nil {
		t.Fatal(err)
	}
	subject, body, ok := parseTemplate(rendered)
	if !ok {
		t.Fatal("translated embedded template did not parse")
	}
	if !strings.Contains(subject, "Copia de seguridad completada") {
		t.Errorf("subject did not use configured Spanish locale: %q", subject)
	}
	if !strings.Contains(body, "La copia de seguridad se completó correctamente") || !strings.Contains(body, "backup.tar.gz") {
		t.Errorf("body did not translate or interpolate variables: %q", body)
	}
}
