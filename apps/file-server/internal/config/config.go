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
	FTPEnabled        bool
	FTPPort           int
	HTTPPort          int
	FTPUser           string
	FTPPass           string
	FTPPasvURL        string
	FTPPasvMin        int
	FTPPasvMax        int
	DatabaseURL       string
	StorageDriver     string
	LocalStorageRoot  string
	GCSBucket         string
	ShareGCSBucket    string
	ShareGCSPrefix    string
	SharePublicURL    string
	TelegramBotToken  string
	TelegramChatID    string
	WebStaticRoot     string
	WebBasePath       string
	WebDAVEnabled     bool
	WebDAVPath        string
	WebDAVUser        string
	WebDAVPass        string
	WebDAVLockTimeout time.Duration
	WebDAVTrustProxy  bool
	CommandArgs       []string
	AssumeYes         bool
}

func Load(args []string) (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		FTPEnabled:        envBool("FTP_ENABLED", true),
		FTPPort:           envInt("FTP_PORT", 21),
		HTTPPort:          envInt("HTTP_PORT", envInt("PORT", 8080)),
		FTPUser:           envString("FTP_USER", "admin"),
		FTPPass:           envString("FTP_PASS", "admin123"),
		FTPPasvURL:        envString("FTP_PASV_URL", "127.0.0.1"),
		FTPPasvMin:        envInt("FTP_PASV_MIN", 30000),
		FTPPasvMax:        envInt("FTP_PASV_MAX", 30005),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		StorageDriver:     envString("STORAGE_DRIVER", "local"),
		LocalStorageRoot:  envString("LOCAL_STORAGE_ROOT", "./data/objects"),
		GCSBucket:         envString("GCS_BUCKET", ""),
		ShareGCSBucket:    envString("SHARE_GCS_BUCKET", ""),
		ShareGCSPrefix:    envString("SHARE_GCS_PREFIX", "shares"),
		SharePublicURL:    envString("SHARE_PUBLIC_BASE_URL", ""),
		TelegramBotToken:  envString("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:    envString("TELEGRAM_CHAT_ID", ""),
		WebStaticRoot:     envString("WEB_STATIC_ROOT", ""),
		WebBasePath:       envString("WEB_BASE_PATH", "/"),
		WebDAVEnabled:     envBool("WEBDAV_ENABLED", false),
		WebDAVPath:        normalizeWebDAVPath(envString("WEBDAV_PATH", "/dav/")),
		WebDAVUser:        envString("WEBDAV_USER", ""),
		WebDAVPass:        envString("WEBDAV_PASS", ""),
		WebDAVLockTimeout: envDuration("WEBDAV_LOCK_TIMEOUT", 30*time.Minute),
		WebDAVTrustProxy:  envBool("WEBDAV_TRUST_FORWARDED_HEADERS", false),
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

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
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
