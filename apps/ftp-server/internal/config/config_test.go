package config

import (
	"strings"
	"testing"
)

func TestLoadStorageDriverRequirements(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/vfs")
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	t.Setenv("GCS_BUCKET", "")

	t.Run("local does not require GCS bucket", func(t *testing.T) {
		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.StorageDriver != "local" {
			t.Fatalf("StorageDriver = %q, want local", cfg.StorageDriver)
		}
		if cfg.GCSBucket != "" {
			t.Fatalf("GCSBucket = %q, want empty", cfg.GCSBucket)
		}
	})

	t.Run("local requires local root", func(t *testing.T) {
		_, err := Load([]string{"STORAGE_DRIVER=local", "LOCAL_STORAGE_ROOT="})
		if err == nil || !strings.Contains(err.Error(), "LOCAL_STORAGE_ROOT is required") {
			t.Fatalf("Load() error = %v, want LOCAL_STORAGE_ROOT requirement", err)
		}
	})

	t.Run("gcs does not require local root", func(t *testing.T) {
		cfg, err := Load([]string{
			"STORAGE_DRIVER=gcs",
			"GCS_BUCKET=primary-objects",
			"SHARE_GCS_BUCKET=shared-objects",
			"LOCAL_STORAGE_ROOT=",
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.StorageDriver != "gcs" {
			t.Fatalf("StorageDriver = %q, want gcs", cfg.StorageDriver)
		}
		if cfg.GCSBucket != "primary-objects" {
			t.Fatalf("GCSBucket = %q, want primary-objects", cfg.GCSBucket)
		}
		if cfg.ShareGCSBucket != "shared-objects" {
			t.Fatalf("ShareGCSBucket = %q, want shared-objects", cfg.ShareGCSBucket)
		}
	})

	t.Run("gcs requires GCS bucket", func(t *testing.T) {
		_, err := Load([]string{"STORAGE_DRIVER=gcs", "GCS_BUCKET="})
		if err == nil || !strings.Contains(err.Error(), "GCS_BUCKET is required") {
			t.Fatalf("Load() error = %v, want GCS_BUCKET requirement", err)
		}
	})

	t.Run("unsupported driver is rejected", func(t *testing.T) {
		_, err := Load([]string{"STORAGE_DRIVER=s3"})
		if err == nil || !strings.Contains(err.Error(), `unsupported STORAGE_DRIVER "s3"`) {
			t.Fatalf("Load() error = %v, want unsupported driver error", err)
		}
	})
}
