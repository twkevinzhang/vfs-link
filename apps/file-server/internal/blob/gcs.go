package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
)

const gcsResumableEndpoint = "https://storage.googleapis.com/upload/storage/v1"

type GCSStore struct {
	client     *storage.Client
	httpClient *http.Client
	bucket     string
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
	httpClient := http.DefaultClient
	if strings.TrimSpace(os.Getenv("STORAGE_EMULATOR_HOST")) == "" {
		httpClient, err = google.DefaultClient(ctx, storage.ScopeReadWrite)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("create authenticated GCS HTTP client: %w", err)
		}
	}
	return &GCSStore{
		client:     client,
		httpClient: httpClient,
		bucket:     bucket,
	}, nil
}

func (s *GCSStore) StartResumableUpload(ctx context.Context, objectName, contentType, origin string, size, ifGenerationMatch int64) (string, map[string]string, error) {
	location, err := initiateResumableUpload(ctx, s.httpClient, gcsResumableEndpoint, s.bucket, cleanObjectName(objectName), contentType, origin, size, ifGenerationMatch)
	if err != nil {
		return "", nil, err
	}
	return location, resumableUploadHeaders(contentType, size), nil
}

// CancelResumableUpload invalidates an unfinished upload session. It never
// deletes the destination object: cancellation is addressed solely by the
// opaque session URI returned by Cloud Storage.
func (s *GCSStore) CancelResumableUpload(ctx context.Context, sessionURL string) error {
	return cancelResumableUpload(ctx, s.httpClient, sessionURL)
}

func resumableUploadHeaders(contentType string, size int64) map[string]string {
	headers := map[string]string{}
	if strings.TrimSpace(contentType) != "" {
		headers["Content-Type"] = contentType
	}
	if size == 0 {
		headers["Content-Range"] = "bytes */0"
	} else {
		headers["Content-Range"] = fmt.Sprintf("bytes 0-%d/%d", size-1, size)
	}
	return headers
}

func (s *GCSStore) StatObject(ctx context.Context, objectName string) (ObjectInfo, error) {
	attrs, err := s.client.Bucket(s.bucket).Object(cleanObjectName(objectName)).Attrs(ctx)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Name: attrs.Name, Size: attrs.Size, Generation: attrs.Generation}, nil
}

func initiateResumableUpload(ctx context.Context, client *http.Client, endpoint, bucket, objectName, contentType, origin string, size, ifGenerationMatch int64) (string, error) {
	if client == nil {
		return "", errors.New("authenticated HTTP client is required")
	}
	if strings.TrimSpace(bucket) == "" || strings.TrimSpace(objectName) == "" {
		return "", errors.New("bucket and object name are required")
	}
	query := url.Values{}
	query.Set("uploadType", "resumable")
	query.Set("name", objectName)
	query.Set("ifGenerationMatch", strconv.FormatInt(ifGenerationMatch, 10))
	requestURL := strings.TrimRight(endpoint, "/") + "/b/" + url.PathEscape(bucket) + "/o?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Length", "0")
	request.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	if strings.TrimSpace(contentType) != "" {
		request.Header.Set("X-Upload-Content-Type", contentType)
	}
	if origin = strings.TrimSpace(origin); origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("initiate GCS resumable upload: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("initiate GCS resumable upload: %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	location := strings.TrimSpace(response.Header.Get("Location"))
	if location == "" {
		return "", errors.New("initiate GCS resumable upload: response has no Location header")
	}
	return location, nil
}

func cancelResumableUpload(ctx context.Context, client *http.Client, sessionURL string) error {
	if client == nil {
		return errors.New("authenticated HTTP client is required")
	}
	if strings.TrimSpace(sessionURL) == "" {
		return errors.New("resumable upload session URL is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, sessionURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("cancel GCS resumable upload: %w", err)
	}
	defer response.Body.Close()
	// Cloud Storage uses 499 to acknowledge resumable-session cancellation.
	if (response.StatusCode < 200 || response.StatusCode >= 300) && response.StatusCode != 499 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("cancel GCS resumable upload: %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	return nil
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

func (s *GCSStore) NewWriterIfGenerationMatch(ctx context.Context, physicalHash string, generation int64) (io.WriteCloser, error) {
	objectName := cleanObjectName(physicalHash)
	conditions := storage.Conditions{GenerationMatch: generation}
	if generation == 0 {
		// The Go client treats GenerationMatch: 0 as unset. DoesNotExist is the
		// explicit form that emits ifGenerationMatch=0 on the JSON request.
		conditions = storage.Conditions{DoesNotExist: true}
	}
	object := s.client.Bucket(s.bucket).Object(objectName).If(conditions)
	return object.NewWriter(ctx), nil
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

func (s *GCSStore) CopyToGCS(
	ctx context.Context,
	physicalHash, destinationBucket, destinationObject string,
	metadata map[string]string,
) error {
	sourceName := cleanObjectName(physicalHash)
	destinationObject = cleanObjectName(destinationObject)
	if sourceName == "" || strings.TrimSpace(destinationBucket) == "" || destinationObject == "" {
		return errors.New("source object, destination bucket, and destination object are required")
	}
	source := s.client.Bucket(s.bucket).Object(sourceName)
	destination := s.client.Bucket(strings.TrimSpace(destinationBucket)).Object(destinationObject)
	copier := destination.CopierFrom(source)
	copier.ContentType = "application/octet-stream"
	copier.Metadata = metadata
	if _, err := copier.Run(ctx); err != nil {
		return fmt.Errorf("copy GCS object: %w", err)
	}
	return nil
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
		if isReservedObject(attrs.Name) {
			continue
		}
		objects = append(objects, ObjectInfo{
			Name:       attrs.Name,
			Size:       attrs.Size,
			Generation: attrs.Generation,
		})
	}
	return objects, nil
}

func cleanObjectName(physicalHash string) string {
	return strings.TrimLeft(physicalHash, "/")
}

var _ GenerationMatchWriterStore = (*GCSStore)(nil)
var _ AbortableWriter = (*storage.Writer)(nil)
