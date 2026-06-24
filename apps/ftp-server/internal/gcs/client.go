package gcs

import (
	"context"
	"fmt"
	"io"
	"path"

	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
)

type ObjectInfo struct {
	Name string
	Size int64
}

type Client struct {
	client *storage.Client
	bucket *storage.BucketHandle
}

func New(ctx context.Context, bucketName string) (*Client, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}
	return &Client{
		client: client,
		bucket: client.Bucket(bucketName),
	}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

func GeneratePhysicalHash(logicPath string) string {
	return uuid.NewString() + path.Ext(logicPath)
}

func (c *Client) NewReader(ctx context.Context, physicalHash string) (io.ReadCloser, error) {
	reader, err := c.bucket.Object(physicalHash).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (c *Client) NewWriter(ctx context.Context, physicalHash string) *storage.Writer {
	writer := c.bucket.Object(physicalHash).NewWriter(ctx)
	writer.ChunkSize = 0
	return writer
}

func (c *Client) Delete(ctx context.Context, physicalHash string) error {
	if physicalHash == "" {
		return nil
	}
	err := c.bucket.Object(physicalHash).Delete(ctx)
	if err == storage.ErrObjectNotExist {
		return nil
	}
	return err
}

func (c *Client) List(ctx context.Context) ([]ObjectInfo, error) {
	iter := c.bucket.Objects(ctx, nil)
	var objects []ObjectInfo
	for {
		attrs, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		objects = append(objects, ObjectInfo{Name: attrs.Name, Size: attrs.Size})
	}
	return objects, nil
}
