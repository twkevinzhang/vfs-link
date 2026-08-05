package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

const legacySchemaVersion = 1

// legacySnapshot exists only in this migration command. The file-server
// runtime deliberately has no legacy monolithic JSON reader or fallback.
type legacySnapshot struct {
	Version    int                `json:"version"`
	NextFileID int                `json:"nextFileId"`
	Files      []db.FileRecord    `json:"files"`
	Shares     []db.ShareRecord   `json:"shares"`
	DAVLocks   []db.DAVLockRecord `json:"davLocks"`
	Uploads    []db.UploadRecord  `json:"uploads"`
}

type snapshotSummary struct {
	Files       int
	Directories int
	Bytes       int64
	Shares      int
	DAVLocks    int
	Uploads     int
	MinID       int
	MaxID       int
}

func decodeLegacy(reader io.Reader) (legacySnapshot, error) {
	decoder := json.NewDecoder(reader)
	var snapshot legacySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return legacySnapshot{}, fmt.Errorf("decode legacy metadata: %w", err)
	}
	if snapshot.Version != legacySchemaVersion {
		return legacySnapshot{}, fmt.Errorf("unsupported legacy metadata version %d", snapshot.Version)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return legacySnapshot{}, err
	}
	return snapshot, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing legacy metadata: %w", err)
	}
	return fmt.Errorf("legacy metadata contains multiple JSON values")
}

func validateLegacy(snapshot legacySnapshot) (snapshotSummary, error) {
	if snapshot.NextFileID < 1 {
		return snapshotSummary{}, fmt.Errorf("nextFileId must be positive")
	}
	ids := make(map[int]string, len(snapshot.Files))
	paths := make(map[string]struct{}, len(snapshot.Files))
	var summary snapshotSummary
	for _, record := range snapshot.Files {
		if record.ID < 1 {
			return snapshotSummary{}, fmt.Errorf("file %q has invalid id %d", record.LogicPath, record.ID)
		}
		if previous, exists := ids[record.ID]; exists {
			return snapshotSummary{}, fmt.Errorf("duplicate file id %d for %q and %q", record.ID, previous, record.LogicPath)
		}
		ids[record.ID] = record.LogicPath
		logicPath := strings.TrimSpace(record.LogicPath)
		if logicPath == "" || !strings.HasPrefix(logicPath, "/") {
			return snapshotSummary{}, fmt.Errorf("file id %d has invalid logical path %q", record.ID, record.LogicPath)
		}
		if _, exists := paths[logicPath]; exists && record.TrashedAt == nil {
			return snapshotSummary{}, fmt.Errorf("duplicate active logical path %q", logicPath)
		}
		if record.TrashedAt == nil {
			paths[logicPath] = struct{}{}
		}
		if record.Size < 0 {
			return snapshotSummary{}, fmt.Errorf("file %q has negative size", logicPath)
		}
		if record.IsDirectory {
			summary.Directories++
		} else {
			if strings.TrimSpace(record.PhysicalHash) == "" {
				return snapshotSummary{}, fmt.Errorf("file %q has no physical object", logicPath)
			}
			summary.Files++
			summary.Bytes += record.Size
		}
		if summary.MinID == 0 || record.ID < summary.MinID {
			summary.MinID = record.ID
		}
		if record.ID > summary.MaxID {
			summary.MaxID = record.ID
		}
	}
	if summary.MaxID >= snapshot.NextFileID {
		return snapshotSummary{}, fmt.Errorf("nextFileId %d must exceed maximum id %d", snapshot.NextFileID, summary.MaxID)
	}
	summary.Shares = len(snapshot.Shares)
	summary.DAVLocks = len(snapshot.DAVLocks)
	summary.Uploads = len(snapshot.Uploads)
	return summary, nil
}

// canonicalizeImportSnapshot is the schema boundary between absolute v1/v2
// metadata and relative NFC v3 metadata. Active-path collisions are reported
// before any target objects are written.
func canonicalizeImportSnapshot(snapshot db.TreeImportSnapshot) (db.TreeImportSnapshot, error) {
	for index := range snapshot.Records {
		snapshot.Records[index].LogicPath = logicpath.Clean(snapshot.Records[index].LogicPath)
	}
	activeDirectories := make(map[string]struct{})
	for _, record := range snapshot.Records {
		if record.TrashedAt == nil && record.IsDirectory {
			activeDirectories[record.LogicPath] = struct{}{}
		}
	}
	canonicalParent := func(value string) string {
		parts := strings.Split(logicpath.Clean(value), "/")
		if len(parts) < 2 {
			return strings.Join(parts, "/")
		}
		resolved := ""
		for index := 0; index < len(parts)-1; index++ {
			segment := parts[index]
			candidate := segment
			if resolved != "" {
				candidate = resolved + "/" + segment
			}
			if _, exists := activeDirectories[candidate]; !exists {
				alias := strings.TrimSpace(segment)
				aliasCandidate := alias
				if resolved != "" {
					aliasCandidate = resolved + "/" + alias
				}
				if _, aliasExists := activeDirectories[aliasCandidate]; aliasExists {
					candidate = aliasCandidate
				}
			}
			resolved = candidate
		}
		if resolved == "" {
			return parts[len(parts)-1]
		}
		return resolved + "/" + parts[len(parts)-1]
	}

	active := make(map[string]string, len(snapshot.Records))
	for index := range snapshot.Records {
		record := &snapshot.Records[index]
		legacy := record.LogicPath
		record.LogicPath = canonicalParent(record.LogicPath)
		if record.LogicPath == "" {
			return db.TreeImportSnapshot{}, fmt.Errorf("record id %d resolves to the logical root", record.ID)
		}
		if record.TrashedAt == nil {
			if previous, exists := active[record.LogicPath]; exists {
				return db.TreeImportSnapshot{}, fmt.Errorf("canonical path collision %q: %q and %q", record.LogicPath, previous, legacy)
			}
			active[record.LogicPath] = legacy
		}
	}
	for index := range snapshot.Shares {
		snapshot.Shares[index].LogicPath = canonicalParent(snapshot.Shares[index].LogicPath)
	}
	for index := range snapshot.Uploads {
		snapshot.Uploads[index].LogicPath = canonicalParent(snapshot.Uploads[index].LogicPath)
	}
	return snapshot, nil
}
