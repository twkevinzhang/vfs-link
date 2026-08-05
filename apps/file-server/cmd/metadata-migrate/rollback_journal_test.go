package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

func TestRollbackJournalLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	o := options{
		sourceTreeDriver: "gcs", sourceTreeBucket: "metadata", sourceTreePrefix: "_vfs-link-v3",
		targetDriver: "gcs", targetBucket: "metadata", targetPrefix: "_vfs-link-v4",
	}
	journal := newRollbackJournal(o, db.TreeImportSnapshot{SourceSHA256: "abc"}, db.TreeValidation{Active: 12}, now)
	filename := filepath.Join(t.TempDir(), "cutover", "rollback.json")
	if err := prepareRollbackJournal(filename, journal); err != nil {
		t.Fatal(err)
	}
	if err := prepareRollbackJournal(filename, journal); err == nil {
		t.Fatal("prepareRollbackJournal() overwrote an existing journal")
	}
	if err := completeRollbackJournal(filename, journal, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var got rollbackJournal
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.Source.Prefix != "_vfs-link-v3" || got.Target.Prefix != "_vfs-link-v4" || got.Validation.Active != 12 {
		t.Fatalf("rollback journal = %#v", got)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("rollback journal mode = %o, want 600", info.Mode().Perm())
	}
}
