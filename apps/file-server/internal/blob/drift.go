package blob

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

var (
	ErrDriftObjectNotFound = errors.New("drift object not found")
	ErrDriftTargetExists   = errors.New("drift target already exists")
	ErrDriftPrecondition   = errors.New("drift object precondition failed")
)

// DriftObject contains the attributes needed to make a drift plan immutable.
// ListDriftObjects returns these attributes in the same paged listing request;
// callers must not issue one Attrs request per object.
type DriftObject struct {
	Name         string            `json:"name"`
	Size         int64             `json:"size"`
	Generation   int64             `json:"generation"`
	CRC32C       string            `json:"crc32c,omitempty"`
	MD5          string            `json:"md5,omitempty"`
	StorageClass string            `json:"storageClass,omitempty"`
	Created      time.Time         `json:"created,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

func (o DriftObject) Checksum() string {
	if o.CRC32C != "" {
		return "crc32c:" + o.CRC32C
	}
	if o.MD5 != "" {
		return "md5:" + o.MD5
	}
	return ""
}

// DriftObjectStore is deliberately separate from Store: normal reads and
// uploads do not gain permission to copy or generation-conditionally delete.
type DriftObjectStore interface {
	ListDriftObjects(context.Context) ([]DriftObject, error)
	StatDriftObject(context.Context, string) (DriftObject, error)
	CopyDriftObject(context.Context, string, int64, string, map[string]string) (DriftObject, error)
	DeleteDriftObject(context.Context, string, int64) error
}

func driftGCSObject(a *storage.ObjectAttrs) DriftObject {
	return DriftObject{
		Name: a.Name, Size: a.Size, Generation: a.Generation,
		CRC32C: base64.StdEncoding.EncodeToString([]byte{
			byte(a.CRC32C >> 24), byte(a.CRC32C >> 16), byte(a.CRC32C >> 8), byte(a.CRC32C),
		}),
		MD5: base64.StdEncoding.EncodeToString(a.MD5), StorageClass: a.StorageClass,
		Created: a.Created, Metadata: a.Metadata,
	}
}

func classifyDriftGCSError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ErrDriftObjectNotFound
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 404:
			return ErrDriftObjectNotFound
		case 409, 412:
			return ErrDriftPrecondition
		}
	}
	return err
}

func (s *GCSStore) ListDriftObjects(ctx context.Context) ([]DriftObject, error) {
	it := s.client.Bucket(s.bucket).Objects(ctx, nil)
	objects := make([]DriftObject, 0, 1024)
	for {
		a, err := it.Next()
		if err == iterator.Done {
			return objects, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list drift objects: %w", err)
		}
		if !isReservedObject(a.Name) {
			o := driftGCSObject(a)
			// Arbitrary user metadata is not required for classification and can
			// make a 68k-object persisted snapshot needlessly large. Action marker
			// metadata is fetched only by targeted Stat calls during execution.
			o.Metadata = nil
			objects = append(objects, o)
		}
	}
}

func (s *GCSStore) StatDriftObject(ctx context.Context, name string) (DriftObject, error) {
	a, err := s.client.Bucket(s.bucket).Object(cleanObjectName(name)).Attrs(ctx)
	if err != nil {
		return DriftObject{}, classifyDriftGCSError(err)
	}
	return driftGCSObject(a), nil
}

func (s *GCSStore) CopyDriftObject(ctx context.Context, source string, sourceGeneration int64, target string, metadata map[string]string) (DriftObject, error) {
	source = cleanObjectName(source)
	target = cleanObjectName(target)
	if source == "" || target == "" || sourceGeneration <= 0 {
		return DriftObject{}, errors.New("source, source generation, and target are required")
	}
	bucket := s.client.Bucket(s.bucket)
	src := bucket.Object(source).If(storage.Conditions{GenerationMatch: sourceGeneration})
	dst := bucket.Object(target).If(storage.Conditions{DoesNotExist: true})
	copier := dst.CopierFrom(src)
	copier.Metadata = metadata
	a, err := copier.Run(ctx)
	if err != nil {
		classified := classifyDriftGCSError(err)
		if errors.Is(classified, ErrDriftPrecondition) {
			if _, statErr := bucket.Object(target).Attrs(ctx); statErr == nil {
				return DriftObject{}, ErrDriftTargetExists
			}
		}
		return DriftObject{}, fmt.Errorf("copy drift object: %w", classified)
	}
	return driftGCSObject(a), nil
}

func (s *GCSStore) DeleteDriftObject(ctx context.Context, name string, generation int64) error {
	if generation <= 0 {
		return errors.New("source generation is required")
	}
	err := s.client.Bucket(s.bucket).Object(cleanObjectName(name)).If(storage.Conditions{GenerationMatch: generation}).Delete(ctx)
	if err == nil || errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return fmt.Errorf("delete drift source: %w", classifyDriftGCSError(err))
}

func localDriftObject(name, filename string) (DriftObject, error) {
	info, err := os.Stat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return DriftObject{}, ErrDriftObjectNotFound
	}
	if err != nil {
		return DriftObject{}, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return DriftObject{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return DriftObject{}, err
	}
	generation := info.ModTime().UnixNano() ^ info.Size()
	if generation <= 0 {
		generation = info.Size() + 1
	}
	return DriftObject{Name: cleanObjectName(name), Size: info.Size(), Generation: generation, MD5: "sha256-" + hex.EncodeToString(h.Sum(nil)), Created: info.ModTime()}, nil
}

func (s *LocalStore) ListDriftObjects(ctx context.Context) ([]DriftObject, error) {
	listed, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]DriftObject, 0, len(listed))
	for _, item := range listed {
		o, err := localDriftObject(item.Name, s.objectPath(item.Name))
		if err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, nil
}

func (s *LocalStore) StatDriftObject(_ context.Context, name string) (DriftObject, error) {
	return localDriftObject(name, s.objectPath(name))
}

func (s *LocalStore) CopyDriftObject(ctx context.Context, source string, generation int64, target string, _ map[string]string) (DriftObject, error) {
	before, err := s.StatDriftObject(ctx, source)
	if err != nil {
		return DriftObject{}, err
	}
	if before.Generation != generation {
		return DriftObject{}, ErrDriftPrecondition
	}
	sourcePath, targetPath := s.objectPath(source), s.objectPath(target)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return DriftObject{}, err
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		return DriftObject{}, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".drift-copy-*")
	if err != nil {
		return DriftObject{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return DriftObject{}, err
	}
	if err = tmp.Close(); err != nil {
		return DriftObject{}, err
	}
	if err = os.Link(tmpName, targetPath); errors.Is(err, os.ErrExist) {
		return DriftObject{}, ErrDriftTargetExists
	} else if err != nil {
		return DriftObject{}, err
	}
	after, err := s.StatDriftObject(ctx, source)
	if err != nil || after.Generation != generation || after.Checksum() != before.Checksum() {
		_ = os.Remove(targetPath)
		return DriftObject{}, ErrDriftPrecondition
	}
	return s.StatDriftObject(ctx, target)
}

func (s *LocalStore) DeleteDriftObject(ctx context.Context, name string, generation int64) error {
	current, err := s.StatDriftObject(ctx, name)
	if errors.Is(err, ErrDriftObjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Generation != generation {
		return ErrDriftPrecondition
	}
	return s.Delete(ctx, name)
}
