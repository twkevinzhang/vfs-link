package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	FTPPort     int
	FTPUser     string
	FTPPass     string
	FTPPasvURL  string
	FTPPasvMin  int
	FTPPasvMax  int
	DatabaseURL string
	GCSBucket   string
	CommandArgs []string
	AssumeYes   bool
}

func Load(args []string) (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		FTPPort:     envInt("FTP_PORT", 21),
		FTPUser:     envString("FTP_USER", "admin"),
		FTPPass:     envString("FTP_PASS", "admin123"),
		FTPPasvURL:  envString("FTP_PASV_URL", "127.0.0.1"),
		FTPPasvMin:  envInt("FTP_PASV_MIN", 30000),
		FTPPasvMax:  envInt("FTP_PASV_MAX", 30005),
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		GCSBucket:   strings.TrimSpace(os.Getenv("GCS_BUCKET")),
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
	if cfg.GCSBucket == "" {
		return Config{}, fmt.Errorf("GCS_BUCKET is required")
	}
	if cfg.FTPPasvMax < cfg.FTPPasvMin {
		return Config{}, fmt.Errorf("FTP_PASV_MAX must be >= FTP_PASV_MIN")
	}

	if creds := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); creds != "" {
		if abs, err := filepath.Abs(creds); err == nil {
			_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", abs)
		}
	}

	return cfg, nil
}

func (c Config) ListenAddr() string {
	return fmt.Sprintf("0.0.0.0:%d", c.FTPPort)
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
	case "GCS_BUCKET":
		cfg.GCSBucket = value
	case "GOOGLE_APPLICATION_CREDENTIALS":
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", value)
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
