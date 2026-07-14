package config

import (
	"strings"
	"testing"
	"time"
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

func TestLoadProtocolDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/vfs")
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	t.Setenv("FTP_ENABLED", "")
	t.Setenv("WEBDAV_ENABLED", "")
	t.Setenv("WEBDAV_PATH", "")
	t.Setenv("WEBDAV_LOCK_TIMEOUT", "")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("PORT", "9090")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FTPEnabled {
		t.Fatal("FTPEnabled = false, want true")
	}
	if cfg.WebDAVEnabled {
		t.Fatal("WebDAVEnabled = true, want false")
	}
	if cfg.WebDAVPath != "/dav/" {
		t.Fatalf("WebDAVPath = %q, want /dav/", cfg.WebDAVPath)
	}
	if cfg.WebDAVLockTimeout != 30*time.Minute {
		t.Fatalf("WebDAVLockTimeout = %s, want 30m", cfg.WebDAVLockTimeout)
	}
	if cfg.HTTPPort != 9090 {
		t.Fatalf("HTTPPort = %d, want Cloud Run PORT fallback 9090", cfg.HTTPPort)
	}
	if cfg.WebDAVTrustProxy {
		t.Fatal("WebDAVTrustProxy = true, want secure default false")
	}
}

func TestLoadWebDAVOverridesAndValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/vfs")
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	t.Setenv("FTP_ENABLED", "false")
	t.Setenv("WEBDAV_ENABLED", "true")
	t.Setenv("WEBDAV_USER", "env-user")
	t.Setenv("WEBDAV_PASS", "env-pass")
	t.Setenv("WEBDAV_PATH", "files")
	t.Setenv("WEBDAV_LOCK_TIMEOUT", "45m")

	cfg, err := Load([]string{
		"WEBDAV_USER=cli-user",
		"WEBDAV_PATH=/remote",
		"WEBDAV_LOCK_TIMEOUT=2h",
		"WEBDAV_TRUST_FORWARDED_HEADERS=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FTPEnabled {
		t.Fatal("FTPEnabled = true, want false")
	}
	if !cfg.WebDAVEnabled {
		t.Fatal("WebDAVEnabled = false, want true")
	}
	if cfg.WebDAVUser != "cli-user" || cfg.WebDAVPass != "env-pass" {
		t.Fatalf("WebDAV credentials = %q/%q", cfg.WebDAVUser, cfg.WebDAVPass)
	}
	if cfg.WebDAVPath != "/remote/" {
		t.Fatalf("WebDAVPath = %q, want /remote/", cfg.WebDAVPath)
	}
	if cfg.WebDAVLockTimeout != 2*time.Hour {
		t.Fatalf("WebDAVLockTimeout = %s, want 2h", cfg.WebDAVLockTimeout)
	}
	if !cfg.WebDAVTrustProxy {
		t.Fatal("WebDAVTrustProxy = false, want true")
	}

	_, err = Load([]string{"WEBDAV_USER="})
	if err == nil || !strings.Contains(err.Error(), "WEBDAV_USER is required") {
		t.Fatalf("missing user error = %v", err)
	}
	_, err = Load([]string{"WEBDAV_PASS="})
	if err == nil || !strings.Contains(err.Error(), "WEBDAV_PASS is required") {
		t.Fatalf("missing pass error = %v", err)
	}
	_, err = Load([]string{"WEBDAV_LOCK_TIMEOUT=0s"})
	if err == nil || !strings.Contains(err.Error(), "WEBDAV_LOCK_TIMEOUT must be positive") {
		t.Fatalf("invalid timeout error = %v", err)
	}
	for _, reservedPath := range []string{"/", "/api", "/api/files"} {
		_, err = Load([]string{"WEBDAV_PATH=" + reservedPath})
		if err == nil || !strings.Contains(err.Error(), "WEBDAV_PATH must not overlap") {
			t.Fatalf("reserved path %q error = %v", reservedPath, err)
		}
	}
}
