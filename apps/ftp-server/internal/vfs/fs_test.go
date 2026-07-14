package vfs

import "testing"

func TestFSNameIsStorageDriverNeutral(t *testing.T) {
	fs := New(nil, nil)
	if got, want := fs.Name(), "vfs-link"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}
