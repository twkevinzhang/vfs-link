package db

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresDescendantPatternEscapesLIKEWildcards(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "root", path: "", want: "%"},
		{name: "directory", path: "archive", want: "archive/%"},
		{name: "percent", path: "100%", want: `100\%/%`},
		{name: "underscore", path: "a_b", want: `a\_b/%`},
		{name: "backslash", path: `a\b`, want: `a\\b/%`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgresDescendantPattern(tt.path); got != tt.want {
				t.Fatalf("postgresDescendantPattern(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestPostgresRenameErrorMapsUniqueConstraintToPathConflict(t *testing.T) {
	err := postgresRenameError(&pgconn.PgError{Code: "23505"}, "taken.txt")
	if !errors.Is(err, ErrPathConflict) {
		t.Fatalf("postgresRenameError() = %v, want ErrPathConflict", err)
	}
}

func TestClaimableDAVLocksAllowsUnlockedMoveDestination(t *testing.T) {
	active := []DAVLockRecord{{Token: "source-token", Path: "/docs/source.txt", Depth: 0}}
	provided := map[string]struct{}{"source-token": {}}

	matched, ok := claimableDAVLocks(active, []string{"/docs/source.txt", "/docs/renamed.txt"}, provided, "claim")
	if !ok {
		t.Fatal("claimableDAVLocks() rejected an unlocked MOVE destination")
	}
	if _, found := matched["source-token"]; !found || len(matched) != 1 {
		t.Fatalf("claimableDAVLocks() matched = %#v, want only source-token", matched)
	}
}

func TestClaimableDAVLocksRejectsMissingOrHeldToken(t *testing.T) {
	active := []DAVLockRecord{{Token: "source-token", Path: "/docs/source.txt", Depth: 0}}
	if _, ok := claimableDAVLocks(active, []string{"/docs/source.txt"}, nil, "claim"); ok {
		t.Fatal("claimableDAVLocks() accepted a missing token")
	}

	otherClaim := "other-claim"
	active[0].HeldBy = &otherClaim
	provided := map[string]struct{}{"source-token": {}}
	if _, ok := claimableDAVLocks(active, []string{"/docs/source.txt"}, provided, "claim"); ok {
		t.Fatal("claimableDAVLocks() accepted a lock held by another claim")
	}
}
