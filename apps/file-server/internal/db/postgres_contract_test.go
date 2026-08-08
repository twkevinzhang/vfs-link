package db

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresContractURLVariable = "VFS_LINK_POSTGRES_TEST_URL"

func postgresStoreContractFactory() storeContractFactory {
	return storeContractFactory{name: "postgres", open: openPostgresContractStore}
}

func openPostgresContractStore(t *testing.T) Store {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv(postgresContractURLVariable))
	if databaseURL == "" {
		t.Skip(postgresContractURLVariable + " is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("%s must be a PostgreSQL URL", postgresContractURLVariable)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bootstrap, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL contract bootstrap connection: %v", err)
	}
	if err = bootstrap.Ping(ctx); err != nil {
		bootstrap.Close()
		t.Fatalf("ping PostgreSQL contract database: %v", err)
	}

	schema := "vfs_contract_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = bootstrap.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		bootstrap.Close()
		t.Fatalf("create PostgreSQL contract schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	var store Store
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := bootstrap.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+identifier+" CASCADE"); cleanupErr != nil {
			t.Errorf("drop PostgreSQL contract schema %q: %v", schema, cleanupErr)
		}
		bootstrap.Close()
	})

	store, err = NewPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open isolated PostgreSQL contract store: %v", err)
	}
	return store
}
