package blob

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/google/uuid"
)

const (
	DriverLocal = "local"
	DriverGCS   = "gcs"
)

type StoreConfig struct {
	Driver    string
	LocalRoot string
	GCSBucket string
}

type ObjectInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Store interface {
	Close() error
	Driver() string
	Root() string
	NewReader(ctx context.Context, physicalHash string) (io.ReadCloser, error)
	NewRangeReader(ctx context.Context, physicalHash string, offset, length int64) (io.ReadCloser, error)
	NewWriter(ctx context.Context, physicalHash string) (io.WriteCloser, error)
	Delete(ctx context.Context, physicalHash string) error
	List(ctx context.Context) ([]ObjectInfo, error)
}

// GCSObjectCopier is implemented by a GCS-backed store so large share jobs can
// use Cloud Storage's server-side rewrite instead of proxying bytes through the
// file-server instance.
type GCSObjectCopier interface {
	CopyToGCS(ctx context.Context, physicalHash, destinationBucket, destinationObject string, metadata map[string]string) error
}

// DirectUploadStore supports browser-to-GCS resumable uploads while keeping
// the concrete Cloud Storage client private to this package.
type DirectUploadStore interface {
	Store
	StartResumableUpload(ctx context.Context, objectName, contentType string, size int64) (string, map[string]string, error)
	StatObject(ctx context.Context, objectName string) (ObjectInfo, error)
}

func NewStore(ctx context.Context, cfg StoreConfig) (Store, error) {
	switch strings.TrimSpace(cfg.Driver) {
	case DriverLocal:
		return NewLocal(cfg.LocalRoot)
	case DriverGCS:
		return NewGCS(ctx, cfg.GCSBucket)
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Driver)
	}
}

func GeneratePhysicalHash(logicPath string) string {
	return uuid.NewString() + path.Ext(logicPath)
}

func isReservedObject(name string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(name), "/"), "_vfs-link/")
}
