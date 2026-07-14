package vfs

import (
	"os"
	"path"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type fileInfo struct {
	name        string
	size        int64
	mode        os.FileMode
	modTime     time.Time
	isDirectory bool
}

func rootInfo() os.FileInfo {
	return fileInfo{
		name:        "/",
		mode:        os.ModeDir | 0o777,
		modTime:     time.Now(),
		isDirectory: true,
	}
}

func infoFromRecord(record db.FileRecord) os.FileInfo {
	mode := os.FileMode(0o666)
	if record.IsDirectory {
		mode = os.ModeDir | 0o777
	}

	return fileInfo{
		name:        path.Base(record.LogicPath),
		size:        record.Size,
		mode:        mode,
		modTime:     record.UpdatedAt,
		isDirectory: record.IsDirectory,
	}
}

func (i fileInfo) Name() string {
	return i.name
}

func (i fileInfo) Size() int64 {
	return i.size
}

func (i fileInfo) Mode() os.FileMode {
	return i.mode
}

func (i fileInfo) ModTime() time.Time {
	return i.modTime
}

func (i fileInfo) IsDir() bool {
	return i.isDirectory
}

func (i fileInfo) Sys() any {
	return nil
}
