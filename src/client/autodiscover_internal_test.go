package main

import (
	"encoding/json"
	"testing"
)

func TestAutodiscoverResponse_EnvelopeAndFlat(t *testing.T) {
	wrapped := `{"ok":true,"data":{"cli_min_version":"1.2.0","cli_versions":{"linux-amd64":{"version":"1.3.0","sha256":"ab"}}}}`
	flat := `{"cli_min_version":"1.2.0","cli_versions":{"linux-amd64":{"version":"1.3.0","sha256":"ab"}}}`
	for name, body := range map[string]string{"wrapped": wrapped, "flat": flat} {
		var d autodiscoverResponse
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.CLIMinVersion != "1.2.0" || d.CLIVersions["linux-amd64"].SHA256 != "ab" {
			t.Errorf("%s: decoded %+v", name, d)
		}
	}
}
