package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

type GCSStore struct {
	client *storage.Client
	bucket string
}

func NewGCS(ctx context.Context, bucket string) (*GCSStore, error) {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, errors.New("GCS bucket is required")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &GCSStore{
		client: client,
		bucket: bucket,
	}, nil
}

func (s *GCSStore) Close() error {
	return s.client.Close()
}

func (s *GCSStore) Driver() string {
	return "gcs"
}

func (s *GCSStore) Root() string {
	return "gs://" + s.bucket
}

func (s *GCSStore) NewReader(ctx context.Context, physicalHash string) (io.ReadCloser, error) {
	objectName := cleanObjectName(physicalHash)
	return s.client.Bucket(s.bucket).Object(objectName).NewReader(ctx)
}

func (s *GCSStore) NewRangeReader(ctx context.Context, physicalHash string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("range offset must be non-negative")
	}
	objectName := cleanObjectName(physicalHash)
	return s.client.Bucket(s.bucket).Object(objectName).NewRangeReader(ctx, offset, length)
}

func (s *GCSStore) NewWriter(ctx context.Context, physicalHash string) (io.WriteCloser, error) {
	objectName := cleanObjectName(physicalHash)
	return s.client.Bucket(s.bucket).Object(objectName).NewWriter(ctx), nil
}

func (s *GCSStore) Delete(ctx context.Context, physicalHash string) error {
	objectName := cleanObjectName(physicalHash)
	if objectName == "" {
		return nil
	}
	err := s.client.Bucket(s.bucket).Object(objectName).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

func (s *GCSStore) List(ctx context.Context) ([]ObjectInfo, error) {
	var objects []ObjectInfo
	iter := s.client.Bucket(s.bucket).Objects(ctx, nil)
	for {
		attrs, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		objects = append(objects, ObjectInfo{
			Name: attrs.Name,
			Size: attrs.Size,
		})
	}
	return objects, nil
}

func cleanObjectName(physicalHash string) string {
	return strings.TrimLeft(strings.TrimSpace(physicalHash), "/")
}
