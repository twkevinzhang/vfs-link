package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Generation int64  `json:"generation,omitempty"`
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
	StartResumableUpload(ctx context.Context, objectName, contentType, origin string, size, ifGenerationMatch int64) (string, map[string]string, error)
	CancelResumableUpload(ctx context.Context, sessionURL string) error
	StatObject(ctx context.Context, objectName string) (ObjectInfo, error)
}

// GenerationMatchWriterStore supports a streaming write that is committed
// only if the destination still has the requested generation. Generation zero
// means the destination must not exist.
type GenerationMatchWriterStore interface {
	Store
	NewWriterIfGenerationMatch(context.Context, string, int64) (io.WriteCloser, error)
	StatObject(context.Context, string) (ObjectInfo, error)
}

// AbortableWriter can discard an unfinished streaming write without publishing
// it to the final object name.
type AbortableWriter interface {
	io.WriteCloser
	CloseWithError(error) error
}

var ErrObjectCollision = errors.New("physical object key already exists")

// NewUploadWriter opens a final-key writer with collision/overwrite safety.
// GCS uses a generation precondition. The local backend performs an existence
// preflight and relies on its single-instance, same-filesystem atomic rename.
// That local preflight is not a cross-process CAS: two concurrent local writers
// can still race between List and rename, which is an accepted local-driver
// limitation rather than a guarantee extended to GCS.
func NewUploadWriter(ctx context.Context, objects Store, objectName string, existingPhysicalHash *string) (io.WriteCloser, error) {
	if conditional, ok := objects.(GenerationMatchWriterStore); ok {
		generation := int64(0)
		if existingPhysicalHash != nil && *existingPhysicalHash == objectName {
			object, err := conditional.StatObject(ctx, objectName)
			if err != nil {
				return nil, fmt.Errorf("stat overwrite target: %w", err)
			}
			if object.Generation <= 0 {
				return nil, errors.New("overwrite target has no storage generation")
			}
			generation = object.Generation
		}
		return conditional.NewWriterIfGenerationMatch(ctx, objectName, generation)
	}

	if existingPhysicalHash == nil || *existingPhysicalHash != objectName {
		listed, err := objects.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range listed {
			if object.Name == objectName {
				return nil, fmt.Errorf("%w: %s", ErrObjectCollision, objectName)
			}
		}
	}
	return objects.NewWriter(ctx, objectName)
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

func isReservedObject(name string) bool {
	name = strings.TrimLeft(name, "/")
	first, _, _ := strings.Cut(name, "/")
	return strings.HasPrefix(strings.ToLower(first), "_vfs-link")
}
