package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	FTPPort          int
	HTTPPort         int
	FTPUser          string
	FTPPass          string
	FTPPasvURL       string
	FTPPasvMin       int
	FTPPasvMax       int
	DatabaseURL      string
	StorageDriver    string
	LocalStorageRoot string
	ShareGCSBucket   string
	ShareGCSPrefix   string
	SharePublicURL   string
	SMTPHost         string
	SMTPPort         int
	SMTPUser         string
	SMTPPass         string
	SMTPFrom         string
	WebStaticRoot    string
	WebBasePath      string
	CommandArgs      []string
	AssumeYes        bool
}

func Load(args []string) (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		FTPPort:          envInt("FTP_PORT", 21),
		HTTPPort:         envInt("HTTP_PORT", 8080),
		FTPUser:          envString("FTP_USER", "admin"),
		FTPPass:          envString("FTP_PASS", "admin123"),
		FTPPasvURL:       envString("FTP_PASV_URL", "127.0.0.1"),
		FTPPasvMin:       envInt("FTP_PASV_MIN", 30000),
		FTPPasvMax:       envInt("FTP_PASV_MAX", 30005),
		DatabaseURL:      strings.TrimSpace(os.Getenv("DATABASE_URL")),
		StorageDriver:    envString("STORAGE_DRIVER", "local"),
		LocalStorageRoot: envString("LOCAL_STORAGE_ROOT", "./data/objects"),
		ShareGCSBucket:   envString("SHARE_GCS_BUCKET", ""),
		ShareGCSPrefix:   envString("SHARE_GCS_PREFIX", "shares"),
		SharePublicURL:   envString("SHARE_PUBLIC_BASE_URL", ""),
		SMTPHost:         envString("SMTP_HOST", ""),
		SMTPPort:         envInt("SMTP_PORT", 587),
		SMTPUser:         envString("SMTP_USER", ""),
		SMTPPass:         envString("SMTP_PASS", ""),
		SMTPFrom:         envString("SMTP_FROM", ""),
		WebStaticRoot:    envString("WEB_STATIC_ROOT", ""),
		WebBasePath:      envString("WEB_BASE_PATH", "/"),
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
	if cfg.StorageDriver != "local" {
		return Config{}, fmt.Errorf("unsupported STORAGE_DRIVER %q", cfg.StorageDriver)
	}
	if cfg.LocalStorageRoot == "" {
		return Config{}, fmt.Errorf("LOCAL_STORAGE_ROOT is required")
	}
	if cfg.FTPPasvMax < cfg.FTPPasvMin {
		return Config{}, fmt.Errorf("FTP_PASV_MAX must be >= FTP_PASV_MIN")
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

func applyOverride(cfg *Config, key, value string) {
	switch key {
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
		cfg.StorageDriver = value
	case "LOCAL_STORAGE_ROOT":
		cfg.LocalStorageRoot = value
	case "SHARE_GCS_BUCKET":
		cfg.ShareGCSBucket = value
	case "SHARE_GCS_PREFIX":
		cfg.ShareGCSPrefix = value
	case "SHARE_PUBLIC_BASE_URL":
		cfg.SharePublicURL = value
	case "SMTP_HOST":
		cfg.SMTPHost = value
	case "SMTP_PORT":
		cfg.SMTPPort = parseInt(value, cfg.SMTPPort)
	case "SMTP_USER":
		cfg.SMTPUser = value
	case "SMTP_PASS":
		cfg.SMTPPass = value
	case "SMTP_FROM":
		cfg.SMTPFrom = value
	case "WEB_STATIC_ROOT":
		cfg.WebStaticRoot = value
	case "WEB_BASE_PATH":
		cfg.WebBasePath = value
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
