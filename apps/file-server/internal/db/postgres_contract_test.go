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

func TestPostgresShareSnapshotLocksSourceAgainstReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := openPostgresContractStore(t)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "locked-share.txt", "generation-one", 4); err != nil {
		t.Fatal(err)
	}
	postgres := store.(*PostgresStore)
	if _, err := postgres.pool.Exec(ctx, `
CREATE FUNCTION delay_contract_share_insert() RETURNS trigger AS $$
BEGIN
  PERFORM pg_sleep(0.75);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER delay_contract_share_insert BEFORE INSERT ON "Share"
FOR EACH ROW EXECUTE FUNCTION delay_contract_share_insert();
`); err != nil {
		t.Fatal(err)
	}

	shareDone := make(chan error, 1)
	go func() {
		_, err := store.CreateShareFromSnapshot(ctx, ShareRecord{
			ID: "locked-share", LogicPath: "locked-share.txt", PhysicalHash: "generation-one",
			FileName: "locked-share.txt", Size: 4, DestinationObject: "shares/locked", ShareURL: "https://example.test/locked", Status: "draft",
		})
		shareDone <- err
	}()
	time.Sleep(100 * time.Millisecond)

	expected := "generation-one"
	started := time.Now()
	previous, replaced, err := store.ReplaceFileConditional(ctx, "locked-share.txt", "generation-two", 4, &expected, false)
	elapsed := time.Since(started)
	if err != nil || !replaced || previous != expected {
		t.Fatalf("ReplaceFileConditional = previous %q, replaced %t, err %v", previous, replaced, err)
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("replacement waited only %s; Share source lock was not held through commit", elapsed)
	}
	if err := <-shareDone; err != nil {
		t.Fatal(err)
	}
	referenced, err := store.IsObjectReferenced(ctx, expected, "")
	if err != nil || !referenced {
		t.Fatalf("committed Share reference = %t, err %v", referenced, err)
	}
}
