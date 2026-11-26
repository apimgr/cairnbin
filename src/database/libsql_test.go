package database

import "testing"

func TestBuildLibSQLURLUsesConfiguredToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		url   string
		token string
		want  string
	}{
		{
			name:  "separate token",
			url:   "https://database.example.com",
			token: "secret-token",
			want:  "https://database.example.com?authToken=secret-token",
		},
		{
			name:  "existing URL token wins",
			url:   "libsql://database.example.com?authToken=url-token",
			token: "separate-token",
			want:  "libsql://database.example.com?authToken=url-token",
		},
		{
			name:  "token appends to existing query",
			url:   "https://database.example.com?tls=true",
			token: "secret-token",
			want:  "https://database.example.com?tls=true&authToken=secret-token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildLibSQLURL(tc.url, tc.token); got != tc.want {
				t.Fatalf("buildLibSQLURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
