package blob

import (
	"context"
	"strings"
	"testing"
)

func TestNewStoreLocal(t *testing.T) {
	t.Parallel()

	store, err := NewStore(context.Background(), StoreConfig{
		Driver:    DriverLocal,
		LocalRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.Driver() != DriverLocal {
		t.Fatalf("Driver() = %q, want %q", store.Driver(), DriverLocal)
	}
}

func TestNewStoreGCS(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "http://127.0.0.1:4443")

	store, err := NewStore(context.Background(), StoreConfig{
		Driver:    DriverGCS,
		GCSBucket: "primary-objects",
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.Driver() != DriverGCS {
		t.Fatalf("Driver() = %q, want %q", store.Driver(), DriverGCS)
	}
	if store.Root() != "gs://primary-objects" {
		t.Fatalf("Root() = %q, want gs://primary-objects", store.Root())
	}
}

func TestNewStoreRejectsUnsupportedDriver(t *testing.T) {
	t.Parallel()

	_, err := NewStore(context.Background(), StoreConfig{Driver: "s3"})
	if err == nil || !strings.Contains(err.Error(), `unsupported storage driver "s3"`) {
		t.Fatalf("NewStore() error = %v, want unsupported driver error", err)
	}
}
