package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

const treeCASAttempts = 8

var ErrMetadataConflict = errors.New("metadata changed concurrently")

func isGCSNotFound(err error) bool {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return true
	}
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == 404
}
func classifyGCSSaveError(err error) error {
	if err == nil {
		return nil
	}
	var e *googleapi.Error
	if errors.As(err, &e) && (e.Code == 409 || e.Code == 412) {
		return ErrMetadataConflict
	}
	return fmt.Errorf("write GCS metadata: %w", err)
}

type treeObject struct {
	Data       []byte
	Generation int64
}

type treeBackend interface {
	Get(context.Context, string) (treeObject, bool, error)
	Put(context.Context, string, []byte, *int64) (int64, error)
	Delete(context.Context, string, *int64) error
	List(context.Context, string) ([]string, error)
	Close() error
}

type localTreeBackend struct {
	root string
}

var localTreeLocks sync.Map

func newLocalTreeBackend(root string) (*localTreeBackend, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("metadata local root is required")
	}
	return &localTreeBackend{root: abs}, nil
}
func (b *localTreeBackend) lockFor(key string) *sync.Mutex {
	name, _ := b.filename(key)
	lock, _ := localTreeLocks.LoadOrStore(name, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
func (b *localTreeBackend) filename(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(key, "/")))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid metadata key %q", key)
	}
	return filepath.Join(b.root, clean), nil
}
func localGeneration(data []byte) int64 {
	sum := sha256.Sum256(data)
	var n int64
	for _, v := range sum[:8] {
		n = n<<8 | int64(v)
	}
	if n == 0 {
		return 1
	}
	return n
}
func (b *localTreeBackend) Get(_ context.Context, key string) (treeObject, bool, error) {
	mu := b.lockFor(key)
	mu.Lock()
	defer mu.Unlock()
	return b.getUnlocked(key)
}
func (b *localTreeBackend) getUnlocked(key string) (treeObject, bool, error) {
	name, err := b.filename(key)
	if err != nil {
		return treeObject{}, false, err
	}
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return treeObject{}, false, nil
	}
	if err != nil {
		return treeObject{}, false, err
	}
	return treeObject{Data: data, Generation: localGeneration(data)}, true, nil
}
func (b *localTreeBackend) Put(_ context.Context, key string, data []byte, expected *int64) (int64, error) {
	mu := b.lockFor(key)
	mu.Lock()
	defer mu.Unlock()
	name, err := b.filename(key)
	if err != nil {
		return 0, err
	}
	if expected != nil {
		old, exists, err := b.getUnlocked(key)
		if err != nil {
			return 0, err
		}
		if (*expected == 0 && exists) || (*expected != 0 && (!exists || old.Generation != *expected)) {
			return 0, ErrMetadataConflict
		}
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".tree-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return 0, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, name); err != nil {
		return 0, err
	}
	return localGeneration(data), nil
}
func (b *localTreeBackend) Delete(_ context.Context, key string, expected *int64) error {
	mu := b.lockFor(key)
	mu.Lock()
	defer mu.Unlock()
	name, err := b.filename(key)
	if err != nil {
		return err
	}
	if expected != nil {
		old, ok, err := b.getUnlocked(key)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if old.Generation != *expected {
			return ErrMetadataConflict
		}
	}
	err = os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (b *localTreeBackend) List(_ context.Context, prefix string) ([]string, error) {
	base, err := b.filename(prefix)
	if err != nil {
		return nil, err
	}
	var keys []string
	err = filepath.WalkDir(base, func(name string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return fs.SkipDir
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.root, name)
		if err != nil {
			return err
		}
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(keys)
	return keys, err
}
func (*localTreeBackend) Close() error { return nil }

type gcsTreeBackend struct {
	client *storage.Client
	bucket string
	owns   bool
}

func (b *gcsTreeBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	r, err := b.client.Bucket(b.bucket).Object(key).NewReader(ctx)
	if isGCSNotFound(err) {
		return treeObject{}, false, nil
	}
	if err != nil {
		return treeObject{}, false, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return treeObject{}, false, err
	}
	return treeObject{Data: data, Generation: r.Attrs.Generation}, true, nil
}
func (b *gcsTreeBackend) Put(ctx context.Context, key string, data []byte, expected *int64) (int64, error) {
	o := b.client.Bucket(b.bucket).Object(key)
	if expected != nil {
		if *expected == 0 {
			o = o.If(storage.Conditions{DoesNotExist: true})
		} else {
			o = o.If(storage.Conditions{GenerationMatch: *expected})
		}
	}
	w := o.NewWriter(ctx)
	w.ContentType = "application/json"
	w.CacheControl = "no-store"
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return b.resolveConditionalCreate(ctx, key, data, expected, err)
	}
	if err := w.Close(); err != nil {
		return b.resolveConditionalCreate(ctx, key, data, expected, err)
	}
	return w.Attrs().Generation, nil
}

func (b *gcsTreeBackend) resolveConditionalCreate(ctx context.Context, key string, data []byte, expected *int64, writeErr error) (int64, error) {
	classified := classifyGCSSaveError(writeErr)
	// A conditional create can reach GCS successfully while its response is
	// lost. A transport retry then receives 412 during Write or Close because
	// the first attempt already created the object. Accept that ambiguous result
	// only when the live object is byte-for-byte identical; a different value
	// remains a real concurrent mutation conflict.
	if classified == ErrMetadataConflict && expected != nil && *expected == 0 {
		existing, found, getErr := b.Get(ctx, key)
		if getErr == nil && found && bytes.Equal(existing.Data, data) {
			return existing.Generation, nil
		}
	}
	return 0, classified
}
func (b *gcsTreeBackend) Delete(ctx context.Context, key string, expected *int64) error {
	o := b.client.Bucket(b.bucket).Object(key)
	if expected != nil {
		o = o.If(storage.Conditions{GenerationMatch: *expected})
	}
	err := o.Delete(ctx)
	if isGCSNotFound(err) {
		return nil
	}
	return classifyGCSSaveError(err)
}
func (b *gcsTreeBackend) List(ctx context.Context, prefix string) ([]string, error) {
	it := b.client.Bucket(b.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	var keys []string
	for {
		a, e := it.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, e
		}
		keys = append(keys, a.Name)
	}
	return keys, nil
}
func (b *gcsTreeBackend) Close() error {
	if b.owns {
		return b.client.Close()
	}
	return nil
}
