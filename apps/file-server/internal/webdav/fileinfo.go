package webdav

import (
	"context"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type fileInfo struct {
	name         string
	size         int64
	directory    bool
	updatedAt    time.Time
	physicalHash string
}

func infoFromRecord(record db.FileRecord) fileInfo {
	return fileInfo{
		name:         path.Base(record.LogicPath),
		size:         record.Size,
		directory:    record.IsDirectory,
		updatedAt:    record.UpdatedAt,
		physicalHash: record.PhysicalHash,
	}
}

func rootInfo() fileInfo {
	return fileInfo{name: "/", directory: true, updatedAt: time.Unix(0, 0).UTC()}
}

func (f fileInfo) Name() string { return f.name }
func (f fileInfo) Size() int64  { return f.size }
func (f fileInfo) Mode() os.FileMode {
	if f.directory {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fileInfo) ModTime() time.Time { return f.updatedAt }
func (f fileInfo) IsDir() bool        { return f.directory }
func (f fileInfo) Sys() any           { return nil }

func (f fileInfo) ETag(context.Context) (string, error) {
	if f.physicalHash != "" {
		return fmt.Sprintf("\"%s\"", f.physicalHash), nil
	}
	return fmt.Sprintf("\"dir-%x-%x\"", f.updatedAt.UnixNano(), f.size), nil
}
