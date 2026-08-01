package config

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	FTPEnabled            bool
	FTPPort               int
	HTTPPort              int
	FTPUser               string
	FTPPass               string
	FTPPasvURL            string
	FTPPasvMin            int
	FTPPasvMax            int
	DatabaseURL           string
	DatabaseDriver        string
	MetadataStorageDriver string
	MetadataLocalRoot     string
	MetadataGCSBucket     string
	MetadataPrefix        string
	StorageDriver         string
	LocalStorageRoot      string
	GCSBucket             string
	ShareGCSBucket        string
	ShareGCSPrefix        string
	SharePublicURL        string
	TelegramBotToken      string
	TelegramChatID        string
	WebStaticRoot         string
	WebBasePath           string
	WebDAVEnabled         bool
	WebDAVPath            string
	WebDAVUser            string
	WebDAVPass            string
	WebDAVLockTimeout     time.Duration
	WebDAVTrustProxy      bool
	HTTPBasicAuth         bool
	HTTPBasicUser         string
	HTTPBasicPass         string
	HTTPCORSOrigins       string
	UploadSessionTTL      time.Duration
	UploadMaxBytes        int64
	PubSubDriver          string
	GCPProjectID          string
	PubSubTopic           string
	PubSubAudience        string
	PubSubPushEmail       string
	CommandArgs           []string
	AssumeYes             bool
	MaintenanceMode       bool
	DriftEnabled          bool
}

func Load(args []string) (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		FTPEnabled:            envBool("FTP_ENABLED", true),
		FTPPort:               envInt("FTP_PORT", 21),
		HTTPPort:              envInt("HTTP_PORT", envInt("PORT", 8080)),
		FTPUser:               envString("FTP_USER", "admin"),
		FTPPass:               envString("FTP_PASS", "admin123"),
		FTPPasvURL:            envString("FTP_PASV_URL", "127.0.0.1"),
		FTPPasvMin:            envInt("FTP_PASV_MIN", 30000),
		FTPPasvMax:            envInt("FTP_PASV_MAX", 30005),
		DatabaseURL:           strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DatabaseDriver:        envString("DB_DRIVER", "postgres"),
		MetadataStorageDriver: envString("METADATA_STORAGE_DRIVER", "local"),
		MetadataLocalRoot:     envString("METADATA_LOCAL_ROOT", "./data/metadata"),
		MetadataGCSBucket:     envString("METADATA_GCS_BUCKET", ""),
		MetadataPrefix:        envString("METADATA_PREFIX", "_vfs-link-v3"),
		StorageDriver:         envString("STORAGE_DRIVER", "local"),
		LocalStorageRoot:      envString("LOCAL_STORAGE_ROOT", "./data/objects"),
		GCSBucket:             envString("GCS_BUCKET", ""),
		ShareGCSBucket:        envString("SHARE_GCS_BUCKET", ""),
		ShareGCSPrefix:        envString("SHARE_GCS_PREFIX", "shares"),
		SharePublicURL:        envString("SHARE_PUBLIC_BASE_URL", ""),
		TelegramBotToken:      envString("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:        envString("TELEGRAM_CHAT_ID", ""),
		WebStaticRoot:         envString("WEB_STATIC_ROOT", ""),
		WebBasePath:           envString("WEB_BASE_PATH", "/"),
		WebDAVEnabled:         envBool("WEBDAV_ENABLED", false),
		WebDAVPath:            normalizeWebDAVPath(envString("WEBDAV_PATH", "/dav/")),
		WebDAVUser:            envString("WEBDAV_USER", ""),
		WebDAVPass:            envString("WEBDAV_PASS", ""),
		WebDAVLockTimeout:     envDuration("WEBDAV_LOCK_TIMEOUT", 30*time.Minute),
		WebDAVTrustProxy:      envBool("WEBDAV_TRUST_FORWARDED_HEADERS", false),
		HTTPBasicAuth:         envBool("HTTP_BASIC_AUTH_ENABLED", false),
		HTTPBasicUser:         envString("HTTP_BASIC_AUTH_USER", ""),
		HTTPBasicPass:         envString("HTTP_BASIC_AUTH_PASS", ""),
		HTTPCORSOrigins:       envString("HTTP_CORS_ORIGINS", ""),
		UploadSessionTTL:      envDuration("UPLOAD_SESSION_TTL", 24*time.Hour),
		UploadMaxBytes:        envInt64("UPLOAD_MAX_BYTES", 50*1024*1024*1024),
		PubSubDriver:          envString("PUB_SUB_DRIVER", "goroutine"),
		GCPProjectID:          envString("GCP_PROJECT_ID", ""),
		PubSubTopic:           envString("PUB_SUB_TOPIC", ""),
		PubSubAudience:        envString("PUB_SUB_PUSH_AUDIENCE", ""),
		PubSubPushEmail:       envString("PUB_SUB_PUSH_SERVICE_ACCOUNT", ""),
		MaintenanceMode:       envBool("MAINTENANCE_MODE", false),
		DriftEnabled:          envBool("DRIFT_ENABLED", false),
	}

	for _, arg := range args {
		switch {
		case arg == "--yes" || arg == "-y":
			cfg.AssumeYes = true
		case strings.Contains(arg, "="):
			parts := strings.SplitN(arg, "=", 2)
			applyOverride(&cfg, parts[0], parts[1])
		default:
			cfg.CommandArgs = append(cfg.CommandArgs, arg)
		}
	}

	switch cfg.DatabaseDriver {
	case "postgres":
		if cfg.DatabaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required when DB_DRIVER=postgres")
		}
	case "json":
		cfg.MetadataPrefix = path.Clean(strings.TrimLeft(cfg.MetadataPrefix, "/"))
		if cfg.MetadataPrefix != "_vfs-link" && cfg.MetadataPrefix != "_vfs-link-v2" && cfg.MetadataPrefix != "_vfs-link-v3" {
			return Config{}, fmt.Errorf("METADATA_PREFIX must be one of the reserved prefixes _vfs-link, _vfs-link-v2, or _vfs-link-v3")
		}
		switch cfg.MetadataStorageDriver {
		case "local":
			if strings.TrimSpace(cfg.MetadataLocalRoot) == "" {
				return Config{}, fmt.Errorf("METADATA_LOCAL_ROOT is required when METADATA_STORAGE_DRIVER=local")
			}
		case "gcs":
			if strings.TrimSpace(cfg.MetadataGCSBucket) == "" {
				return Config{}, fmt.Errorf("METADATA_GCS_BUCKET is required when METADATA_STORAGE_DRIVER=gcs")
			}
		default:
			return Config{}, fmt.Errorf("unsupported METADATA_STORAGE_DRIVER %q", cfg.MetadataStorageDriver)
		}
	default:
		return Config{}, fmt.Errorf("unsupported DB_DRIVER %q", cfg.DatabaseDriver)
	}
	switch cfg.StorageDriver {
	case "local":
		if cfg.LocalStorageRoot == "" {
			return Config{}, fmt.Errorf("LOCAL_STORAGE_ROOT is required when STORAGE_DRIVER=local")
		}
	case "gcs":
		if cfg.GCSBucket == "" {
			return Config{}, fmt.Errorf("GCS_BUCKET is required when STORAGE_DRIVER=gcs")
		}
	default:
		return Config{}, fmt.Errorf("unsupported STORAGE_DRIVER %q", cfg.StorageDriver)
	}
	if cfg.FTPPasvMax < cfg.FTPPasvMin {
		return Config{}, fmt.Errorf("FTP_PASV_MAX must be >= FTP_PASV_MIN")
	}
	if cfg.WebDAVEnabled {
		if strings.TrimSpace(cfg.WebDAVUser) == "" {
			return Config{}, fmt.Errorf("WEBDAV_USER is required when WEBDAV_ENABLED=true")
		}
		if strings.TrimSpace(cfg.WebDAVPass) == "" {
			return Config{}, fmt.Errorf("WEBDAV_PASS is required when WEBDAV_ENABLED=true")
		}
		if cfg.WebDAVPath == "/" || strings.HasPrefix(cfg.WebDAVPath, "/api/") {
			return Config{}, fmt.Errorf("WEBDAV_PATH must not overlap / or /api/")
		}
	}
	if cfg.WebDAVLockTimeout <= 0 {
		return Config{}, fmt.Errorf("WEBDAV_LOCK_TIMEOUT must be positive")
	}
	if cfg.HTTPBasicAuth {
		if strings.TrimSpace(cfg.HTTPBasicUser) == "" {
			return Config{}, fmt.Errorf("HTTP_BASIC_AUTH_USER is required when HTTP_BASIC_AUTH_ENABLED=true")
		}
		if strings.TrimSpace(cfg.HTTPBasicPass) == "" {
			return Config{}, fmt.Errorf("HTTP_BASIC_AUTH_PASS is required when HTTP_BASIC_AUTH_ENABLED=true")
		}
	}
	if cfg.UploadSessionTTL <= 0 {
		return Config{}, fmt.Errorf("UPLOAD_SESSION_TTL must be positive")
	}
	if cfg.UploadMaxBytes <= 0 {
		return Config{}, fmt.Errorf("UPLOAD_MAX_BYTES must be positive")
	}
	switch cfg.PubSubDriver {
	case "goroutine":
	case "pubsub":
		if strings.TrimSpace(cfg.GCPProjectID) == "" {
			return Config{}, fmt.Errorf("GCP_PROJECT_ID is required when PUB_SUB_DRIVER=pubsub")
		}
		if strings.TrimSpace(cfg.PubSubTopic) == "" {
			return Config{}, fmt.Errorf("PUB_SUB_TOPIC is required when PUB_SUB_DRIVER=pubsub")
		}
		if strings.TrimSpace(cfg.PubSubAudience) == "" {
			return Config{}, fmt.Errorf("PUB_SUB_PUSH_AUDIENCE is required when PUB_SUB_DRIVER=pubsub")
		}
		if strings.TrimSpace(cfg.PubSubPushEmail) == "" {
			return Config{}, fmt.Errorf("PUB_SUB_PUSH_SERVICE_ACCOUNT is required when PUB_SUB_DRIVER=pubsub")
		}
	default:
		return Config{}, fmt.Errorf("unsupported PUB_SUB_DRIVER %q", cfg.PubSubDriver)
	}

	return cfg, nil
}

func (c Config) ListenAddr() string {
	return fmt.Sprintf("0.0.0.0:%d", c.FTPPort)
}

func (c Config) HTTPListenAddr() string {
	return fmt.Sprintf("0.0.0.0:%d", c.HTTPPort)
}

func envString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	return parseInt(strings.TrimSpace(os.Getenv(key)), fallback)
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	return parseBool(strings.TrimSpace(os.Getenv(key)), fallback)
}

func envDuration(key string, fallback time.Duration) time.Duration {
	return parseDuration(strings.TrimSpace(os.Getenv(key)), fallback)
}

func applyOverride(cfg *Config, key, value string) {
	switch key {
	case "FTP_ENABLED":
		cfg.FTPEnabled = parseBool(value, cfg.FTPEnabled)
	case "FTP_PORT":
		cfg.FTPPort = parseInt(value, cfg.FTPPort)
	case "HTTP_PORT":
		cfg.HTTPPort = parseInt(value, cfg.HTTPPort)
	case "FTP_USER":
		cfg.FTPUser = value
	case "FTP_PASS":
		cfg.FTPPass = value
	case "FTP_PASV_URL":
		cfg.FTPPasvURL = value
	case "FTP_PASV_MIN":
		cfg.FTPPasvMin = parseInt(value, cfg.FTPPasvMin)
	case "FTP_PASV_MAX":
		cfg.FTPPasvMax = parseInt(value, cfg.FTPPasvMax)
	case "DATABASE_URL":
		cfg.DatabaseURL = value
	case "DB_DRIVER":
		cfg.DatabaseDriver = strings.TrimSpace(value)
	case "METADATA_STORAGE_DRIVER":
		cfg.MetadataStorageDriver = strings.TrimSpace(value)
	case "METADATA_LOCAL_ROOT":
		cfg.MetadataLocalRoot = strings.TrimSpace(value)
	case "METADATA_GCS_BUCKET":
		cfg.MetadataGCSBucket = strings.TrimSpace(value)
	case "METADATA_PREFIX":
		cfg.MetadataPrefix = strings.TrimSpace(value)
	case "STORAGE_DRIVER":
		cfg.StorageDriver = strings.TrimSpace(value)
	case "LOCAL_STORAGE_ROOT":
		cfg.LocalStorageRoot = strings.TrimSpace(value)
	case "GCS_BUCKET":
		cfg.GCSBucket = strings.TrimSpace(value)
	case "SHARE_GCS_BUCKET":
		cfg.ShareGCSBucket = value
	case "SHARE_GCS_PREFIX":
		cfg.ShareGCSPrefix = value
	case "SHARE_PUBLIC_BASE_URL":
		cfg.SharePublicURL = value
	case "TELEGRAM_BOT_TOKEN":
		cfg.TelegramBotToken = value
	case "TELEGRAM_CHAT_ID":
		cfg.TelegramChatID = value
	case "WEB_STATIC_ROOT":
		cfg.WebStaticRoot = value
	case "WEB_BASE_PATH":
		cfg.WebBasePath = value
	case "WEBDAV_ENABLED":
		cfg.WebDAVEnabled = parseBool(value, cfg.WebDAVEnabled)
	case "WEBDAV_PATH":
		cfg.WebDAVPath = normalizeWebDAVPath(value)
	case "WEBDAV_USER":
		cfg.WebDAVUser = value
	case "WEBDAV_PASS":
		cfg.WebDAVPass = value
	case "WEBDAV_LOCK_TIMEOUT":
		cfg.WebDAVLockTimeout = parseDuration(value, cfg.WebDAVLockTimeout)
	case "WEBDAV_TRUST_FORWARDED_HEADERS":
		cfg.WebDAVTrustProxy = parseBool(value, cfg.WebDAVTrustProxy)
	case "HTTP_BASIC_AUTH_ENABLED":
		cfg.HTTPBasicAuth = parseBool(value, cfg.HTTPBasicAuth)
	case "HTTP_BASIC_AUTH_USER":
		cfg.HTTPBasicUser = value
	case "HTTP_BASIC_AUTH_PASS":
		cfg.HTTPBasicPass = value
	case "HTTP_CORS_ORIGINS":
		cfg.HTTPCORSOrigins = strings.TrimSpace(value)
	case "UPLOAD_SESSION_TTL":
		cfg.UploadSessionTTL = parseDuration(value, cfg.UploadSessionTTL)
	case "UPLOAD_MAX_BYTES":
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			cfg.UploadMaxBytes = parsed
		}
	case "PUB_SUB_DRIVER":
		cfg.PubSubDriver = strings.TrimSpace(value)
	case "GCP_PROJECT_ID":
		cfg.GCPProjectID = strings.TrimSpace(value)
	case "PUB_SUB_TOPIC":
		cfg.PubSubTopic = strings.TrimSpace(value)
	case "PUB_SUB_PUSH_AUDIENCE":
		cfg.PubSubAudience = strings.TrimSpace(value)
	case "PUB_SUB_PUSH_SERVICE_ACCOUNT":
		cfg.PubSubPushEmail = strings.TrimSpace(value)
	case "MAINTENANCE_MODE":
		cfg.MaintenanceMode = parseBool(value, cfg.MaintenanceMode)
	case "DRIFT_ENABLED":
		cfg.DriftEnabled = parseBool(value, cfg.DriftEnabled)
	}
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseBool(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func parseDuration(value string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func normalizeWebDAVPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/dav/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = path.Clean(value)
	if value != "/" {
		value += "/"
	}
	return value
}
