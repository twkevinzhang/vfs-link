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
	NewWriter(ctx context.Context, physicalHash string) (io.WriteCloser, error)
	Delete(ctx context.Context, physicalHash string) error
	List(ctx context.Context) ([]ObjectInfo, error)
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
