package db

import "testing"

func TestDAVLocksConflict(t *testing.T) {
	tests := []struct {
		name   string
		first  DAVLockRecord
		second DAVLockRecord
		want   bool
	}{
		{
			name:   "same path always conflicts",
			first:  DAVLockRecord{Path: "/documents", Depth: 0},
			second: DAVLockRecord{Path: "/documents", Depth: 0},
			want:   true,
		},
		{
			name:   "infinite parent conflicts with child",
			first:  DAVLockRecord{Path: "/documents", Depth: -1},
			second: DAVLockRecord{Path: "/documents/report.txt", Depth: 0},
			want:   true,
		},
		{
			name:   "infinite child conflicts with infinite parent",
			first:  DAVLockRecord{Path: "/documents/report.txt", Depth: -1},
			second: DAVLockRecord{Path: "/documents", Depth: -1},
			want:   true,
		},
		{
			name:   "zero depth parent permits child",
			first:  DAVLockRecord{Path: "/documents", Depth: 0},
			second: DAVLockRecord{Path: "/documents/report.txt", Depth: 0},
			want:   false,
		},
		{
			name:   "path segment prefix is not ancestor",
			first:  DAVLockRecord{Path: "/doc", Depth: -1},
			second: DAVLockRecord{Path: "/documents/report.txt", Depth: 0},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := davLocksConflict(tt.first, tt.second); got != tt.want {
				t.Fatalf("davLocksConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDAVLockCoversPath(t *testing.T) {
	rootLock := DAVLockRecord{Path: "/", Depth: -1}
	if !davLockCoversPath(rootLock, "/documents/report.txt") {
		t.Fatal("infinite root lock should cover descendants")
	}
	zeroDepth := DAVLockRecord{Path: "/documents", Depth: 0}
	if !davLockCoversPath(zeroDepth, "/documents") {
		t.Fatal("zero-depth lock should cover its root")
	}
	if davLockCoversPath(zeroDepth, "/documents/report.txt") {
		t.Fatal("zero-depth lock should not cover descendants")
	}
}

func TestCleanDAVPath(t *testing.T) {
	if got := cleanDAVPath(" documents/../reports/ "); got != "/reports" {
		t.Fatalf("cleanDAVPath() = %q, want /reports", got)
	}
}
