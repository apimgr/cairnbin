package config

import "testing"

// TestParseSize_SingleLetterSuffix covers the spec's compact forms ("1G",
// "512M") used by server.restore and server.backup limits.
func TestParseSize_SingleLetterSuffix(t *testing.T) {
	cases := map[string]int64{
		"1G": 1 << 30, "4g": 4 << 30, "512M": 512 << 20, "64k": 64 << 10, "2T": 2 << 40,
		"1gb": 1 << 30, "5": 5 << 20, "10b": 10,
	}
	for in, want := range cases {
		got, err := ParseSize(in, -1)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseSize("12x", -1); err == nil {
		t.Error("ParseSize(\"12x\") should fail")
	}
}
