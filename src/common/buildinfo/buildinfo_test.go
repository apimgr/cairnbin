package buildinfo

import "testing"

func TestStampForDefaultsAndTruncation(t *testing.T) {
	cases := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{"empty falls back", "", "", "devel-0000000"},
		{"unknown falls back", "unknown", "UNKNOWN", "devel-0000000"},
		{"commit truncated to short sha", "1.2.3", "abcdef0123456789", "1.2.3-abcdef0"},
		{"unsafe characters stripped", "1.2.3 rc/1", "ab cd ef0", "1.2.3rc1-abcdef0"},
		{"short commit kept as-is", "2.0.0", "abc", "2.0.0-abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stampFor(tc.version, tc.commit); got != tc.want {
				t.Fatalf("stampFor(%q, %q) = %q, want %q", tc.version, tc.commit, got, tc.want)
			}
		})
	}
}

func TestAssetStampReflectsSet(t *testing.T) {
	Set("9.9.9", "deadbeefcafe")
	if got, want := AssetStamp(), "9.9.9-deadbee"; got != want {
		t.Fatalf("AssetStamp() = %q, want %q", got, want)
	}
}

func TestAssetURL(t *testing.T) {
	Set("1.0.0", "abcdef0")
	cases := []struct {
		name   string
		prefix string
		path   string
		want   string
	}{
		{"plain path", "", "/static/css/common.css", "/static/css/common.css?v=1.0.0-abcdef0"},
		{"with prefix", "/base", "/static/js/app.js", "/base/static/js/app.js?v=1.0.0-abcdef0"},
		{"missing leading slash", "", "static/x.css", "/static/x.css?v=1.0.0-abcdef0"},
		{"existing query uses ampersand", "", "/static/x.css?a=1", "/static/x.css?a=1&v=1.0.0-abcdef0"},
		{"empty path returns prefix", "/base", "", "/base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AssetURL(tc.prefix, tc.path); got != tc.want {
				t.Fatalf("AssetURL(%q, %q) = %q, want %q", tc.prefix, tc.path, got, tc.want)
			}
		})
	}
}
