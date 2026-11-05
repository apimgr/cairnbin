package data_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/data"
)

// TestLanguages_ReturnsList verifies Languages() returns a non-empty slice.
func TestLanguages_ReturnsList(t *testing.T) {
	langs := data.Languages()
	if len(langs) == 0 {
		t.Fatal("Languages() returned empty slice")
	}
}

// TestLanguages_TextFirst verifies that "text" is the first element (spec
// requirement: "text always first").
func TestLanguages_TextFirst(t *testing.T) {
	langs := data.Languages()
	if langs[0] != "text" {
		t.Errorf("first language: got %q, want %q", langs[0], "text")
	}
}

// TestLanguages_Strings verifies all returned values are non-empty strings.
func TestLanguages_Strings(t *testing.T) {
	langs := data.Languages()
	for i, l := range langs {
		if l == "" {
			t.Errorf("langs[%d] is an empty string", i)
		}
	}
}

// TestLanguages_Idempotent verifies two calls return the same length.
func TestLanguages_Idempotent(t *testing.T) {
	a := data.Languages()
	b := data.Languages()
	if len(a) != len(b) {
		t.Errorf("Languages() not idempotent: len %d vs %d", len(a), len(b))
	}
}
