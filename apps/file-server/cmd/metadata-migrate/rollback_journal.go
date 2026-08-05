package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const rollbackJournalVersion = 1

type rollbackJournal struct {
	Version          int               `json:"version"`
	Status           string            `json:"status"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Source           rollbackEndpoint  `json:"source"`
	Target           rollbackEndpoint  `json:"target"`
	SourceSHA256     string            `json:"sourceSha256,omitempty"`
	SourceGeneration int64             `json:"sourceGeneration,omitempty"`
	Validation       db.TreeValidation `json:"validation"`
}

type rollbackEndpoint struct {
	Driver          string `json:"driver"`
	LocalRoot       string `json:"localRoot,omitempty"`
	GCSBucket       string `json:"gcsBucket,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
	GCSObject       string `json:"gcsObject,omitempty"`
	Generation      int64  `json:"generation,omitempty"`
	ShardCount      int    `json:"shardCount,omitempty"`
	ReducerInterval string `json:"reducerInterval,omitempty"`
	MutationMode    string `json:"mutationMode,omitempty"`
}

func newRollbackJournal(o options, snapshot db.TreeImportSnapshot, validation db.TreeValidation, now time.Time) rollbackJournal {
	source := rollbackEndpoint{}
	switch {
	case o.sourceTreeDriver != "":
		source = rollbackEndpoint{Driver: o.sourceTreeDriver, LocalRoot: o.sourceTreeRoot, GCSBucket: o.sourceTreeBucket, Prefix: o.sourceTreePrefix}
	case o.sourceFile != "":
		source = rollbackEndpoint{Driver: "file", LocalRoot: o.sourceFile}
	default:
		source = rollbackEndpoint{Driver: "gcs", GCSBucket: o.sourceBucket, GCSObject: o.sourceObject, Generation: snapshot.SourceGeneration}
	}
	target := rollbackEndpoint{
		Driver: o.targetDriver, Prefix: o.targetPrefix, ShardCount: o.targetShardCount,
		ReducerInterval: o.targetReducerInterval.String(), MutationMode: o.targetMutationMode,
	}
	if o.targetDriver == "local" {
		target.LocalRoot = o.targetRoot
	} else if o.targetDriver == "gcs" {
		target.GCSBucket = o.targetBucket
	}
	return rollbackJournal{
		Version: rollbackJournalVersion, Status: "prepared", CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		Source: source, Target: target, SourceSHA256: snapshot.SourceSHA256,
		SourceGeneration: snapshot.SourceGeneration, Validation: validation,
	}
}

func writeRollbackJournal(filename string, journal rollbackJournal) error {
	if filename == "" {
		return nil
	}
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create rollback journal directory: %w", err)
	}
	payload, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rollback journal: %w", err)
	}
	payload = append(payload, '\n')
	temporary, err := os.CreateTemp(directory, ".metadata-rollback-*.tmp")
	if err != nil {
		return fmt.Errorf("create rollback journal temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect rollback journal: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write rollback journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync rollback journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close rollback journal: %w", err)
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return fmt.Errorf("publish rollback journal: %w", err)
	}
	return nil
}

func prepareRollbackJournal(filename string, journal rollbackJournal) error {
	if filename == "" {
		return nil
	}
	if _, err := os.Lstat(filename); err == nil {
		return fmt.Errorf("rollback journal already exists: %s", filename)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect rollback journal: %w", err)
	}
	return writeRollbackJournal(filename, journal)
}

func completeRollbackJournal(filename string, journal rollbackJournal, now time.Time) error {
	journal.Status = "completed"
	journal.UpdatedAt = now.UTC()
	return writeRollbackJournal(filename, journal)
}
