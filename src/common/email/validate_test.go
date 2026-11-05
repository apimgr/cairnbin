package email

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	m := &Mailer{cfg: &config.EmailConfig{TemplateDir: dir}}
	got, err := m.loadTemplate("test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "bogus_var") {
		t.Error("invalid custom template must be ignored in favor of the default")
	}
}
