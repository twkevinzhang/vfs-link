package blob

import (
	"context"
	"io"
	"path"

	"github.com/google/uuid"
)

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

func GeneratePhysicalHash(logicPath string) string {
	return uuid.NewString() + path.Ext(logicPath)
}
