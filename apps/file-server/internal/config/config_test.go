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

func TestLoadDatabaseDriverRequirements(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	t.Setenv("DATABASE_URL", "")

	cfg, err := Load([]string{
		"DB_DRIVER=json",
		"METADATA_STORAGE_DRIVER=local",
		"METADATA_LOCAL_ROOT=" + t.TempDir(),
		"METADATA_PREFIX=_vfs-link",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseDriver != "json" || cfg.MetadataStorageDriver != "local" || cfg.MetadataPrefix != "_vfs-link" {
		t.Fatalf("tree metadata config = %#v", cfg)
	}
	cfg, err = Load([]string{
		"DB_DRIVER=json",
		"METADATA_STORAGE_DRIVER=local",
		"METADATA_LOCAL_ROOT=" + t.TempDir(),
		"METADATA_PREFIX=_vfs-link-v2",
	})
	if err != nil || cfg.MetadataPrefix != "_vfs-link-v2" {
		t.Fatalf("v2 metadata prefix config = %#v, error = %v", cfg, err)
	}

	_, err = Load([]string{"DB_DRIVER=postgres", "DATABASE_URL="})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required when DB_DRIVER=postgres") {
		t.Fatalf("postgres validation error = %v", err)
	}

	_, err = Load([]string{"DB_DRIVER=json", "METADATA_STORAGE_DRIVER=local", "METADATA_LOCAL_ROOT="})
	if err == nil || !strings.Contains(err.Error(), "METADATA_LOCAL_ROOT is required") {
		t.Fatalf("local metadata validation error = %v", err)
	}

	_, err = Load([]string{"DB_DRIVER=json", "METADATA_STORAGE_DRIVER=gcs", "METADATA_GCS_BUCKET="})
	if err == nil || !strings.Contains(err.Error(), "METADATA_GCS_BUCKET is required") {
		t.Fatalf("GCS metadata validation error = %v", err)
	}

	cfg, err = Load([]string{
		"DB_DRIVER=json",
		"METADATA_STORAGE_DRIVER=gcs",
		"METADATA_GCS_BUCKET=metadata-standard",
		"STORAGE_DRIVER=local",
		"LOCAL_STORAGE_ROOT=" + t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetadataGCSBucket != "metadata-standard" || cfg.StorageDriver != "local" {
		t.Fatalf("independent metadata/object drivers = %#v", cfg)
	}

	_, err = Load([]string{"DB_DRIVER=json", "METADATA_PREFIX=metadata"})
	if err == nil || !strings.Contains(err.Error(), "reserved prefixes") {
		t.Fatalf("metadata prefix validation error = %v", err)
	}

	_, err = Load([]string{"DB_DRIVER=json", "METADATA_STORAGE_DRIVER=s3"})
	if err == nil || !strings.Contains(err.Error(), `unsupported METADATA_STORAGE_DRIVER "s3"`) {
		t.Fatalf("metadata driver validation error = %v", err)
	}

	_, err = Load([]string{"DB_DRIVER=sqlite"})
	if err == nil || !strings.Contains(err.Error(), `unsupported DB_DRIVER "sqlite"`) {
		t.Fatalf("unsupported database driver error = %v", err)
	}
}

func TestLoadHTTPAuthUploadAndPubSub(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.test/vfs")
	t.Setenv("STORAGE_DRIVER", "local")
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())

	cfg, err := Load([]string{
		"HTTP_BASIC_AUTH_ENABLED=true",
		"HTTP_BASIC_AUTH_USER=operator",
		"HTTP_BASIC_AUTH_PASS=secret",
		"UPLOAD_SESSION_TTL=48h",
		"UPLOAD_MAX_BYTES=53687091200",
		"PUB_SUB_DRIVER=pubsub",
		"GCP_PROJECT_ID=example-project",
		"PUB_SUB_TOPIC=vfs-link-share-jobs",
		"PUB_SUB_PUSH_AUDIENCE=https://file-server.example/",
		"PUB_SUB_PUSH_SERVICE_ACCOUNT=push@example.iam.gserviceaccount.com",
		"MAINTENANCE_MODE=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HTTPBasicAuth || cfg.HTTPBasicUser != "operator" || cfg.HTTPBasicPass != "secret" {
		t.Fatalf("HTTP auth config = enabled:%t user:%q", cfg.HTTPBasicAuth, cfg.HTTPBasicUser)
	}
	if cfg.UploadSessionTTL != 48*time.Hour || cfg.UploadMaxBytes != 50*1024*1024*1024 {
		t.Fatalf("upload config = %s/%d", cfg.UploadSessionTTL, cfg.UploadMaxBytes)
	}
	if cfg.PubSubDriver != "pubsub" || cfg.PubSubTopic != "vfs-link-share-jobs" {
		t.Fatalf("Pub/Sub config = %q/%q", cfg.PubSubDriver, cfg.PubSubTopic)
	}
	if !cfg.MaintenanceMode {
		t.Fatal("MaintenanceMode = false, want true")
	}

	_, err = Load([]string{"HTTP_BASIC_AUTH_ENABLED=true", "HTTP_BASIC_AUTH_USER="})
	if err == nil || !strings.Contains(err.Error(), "HTTP_BASIC_AUTH_USER is required") {
		t.Fatalf("HTTP auth validation error = %v", err)
	}

	_, err = Load([]string{"PUB_SUB_DRIVER=pubsub", "GCP_PROJECT_ID="})
	if err == nil || !strings.Contains(err.Error(), "GCP_PROJECT_ID is required") {
		t.Fatalf("Pub/Sub validation error = %v", err)
	}
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
