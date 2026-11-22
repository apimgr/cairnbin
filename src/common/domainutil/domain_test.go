package domainutil

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{" HTTPS://User:pass@ExAmple.COM.:8443/path ", "example.com", true},
		{"https://bücher.example/path", "xn--bcher-kva.example", true},
		{"reader@example.com", "example.com", true},
		{"example.com:443", "example.com", true},
		{"127.0.0.1", "", false},
		{"[2001:db8::1]:443", "", false},
		{"bad domain", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := NormalizeHost(tc.input)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NormalizeHost(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNormalizeEntryAndMatches(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"Example.com.", "example.com", true},
		{"*.example.com", "*.example.com", true},
		{"*.example.co.uk", "*.example.co.uk", true},
		{"*.com", "", false},
		{"*.co.uk", "", false},
		{"https://example.com/path", "", false},
	} {
		got, ok := NormalizeEntry(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeEntry(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		entry string
		host  string
		want  bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "sub.example.com", false},
		{"*.example.com", "sub.example.com", true},
		{"*.example.com", "deep.sub.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "notexample.com", false},
	} {
		if got := Matches(tc.entry, tc.host); got != tc.want {
			t.Errorf("Matches(%q, %q) = %v; want %v", tc.entry, tc.host, got, tc.want)
		}
	}
}
