package db

import "testing"

func TestPostgresDescendantPatternEscapesLIKEWildcards(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "root", path: "/", want: "/%"},
		{name: "directory", path: "/archive", want: "/archive/%"},
		{name: "percent", path: "/100%", want: `/100\%/%`},
		{name: "underscore", path: "/a_b", want: `/a\_b/%`},
		{name: "backslash", path: `/a\b`, want: `/a\\b/%`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresDescendantPattern(tt.path); got != tt.want {
				t.Fatalf("postgresDescendantPattern(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
