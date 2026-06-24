package ftp

import (
	"errors"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/ftp-server/internal/config"
)

func TestGetTLSConfigReturnsErrorWhenTLSIsDisabled(t *testing.T) {
	driver := NewMainDriver(configFixture(), nil, nil, nil)

	tlsConfig, err := driver.GetTLSConfig()
	if err == nil {
		t.Fatal("expected TLS-disabled driver to return an error")
	}
	if !errors.Is(err, errTLSNotConfigured) {
		t.Fatalf("expected errTLSNotConfigured, got %v", err)
	}
	if tlsConfig != nil {
		t.Fatalf("expected nil TLS config when TLS is disabled, got %#v", tlsConfig)
	}
}

func configFixture() config.Config {
	return config.Config{
		FTPPort:          2121,
		HTTPPort:         8080,
		FTPUser:          "admin",
		FTPPass:          "admin123",
		FTPPasvURL:       "127.0.0.1",
		FTPPasvMin:       30000,
		FTPPasvMax:       30005,
		DatabaseURL:      "postgres://example",
		StorageDriver:    "local",
		LocalStorageRoot: "/tmp/vfs-link-test",
	}
}
