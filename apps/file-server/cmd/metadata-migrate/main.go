package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type options struct {
	sourceFile       string
	sourceBucket     string
	sourceObject     string
	sourceGeneration int64
	sourceTreeDriver string
	sourceTreeRoot   string
	sourceTreeBucket string
	sourceTreePrefix string
	targetDriver     string
	targetRoot       string
	targetBucket     string
	targetPrefix     string
	dryRun           bool
	assumeYes        bool
	timeout          time.Duration
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "metadata-migrate:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("metadata-migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	var o options
	flags.StringVar(&o.sourceFile, "source-file", "", "legacy metadata.json file")
	flags.StringVar(&o.sourceBucket, "source-gcs-bucket", "", "legacy metadata GCS bucket")
	flags.StringVar(&o.sourceObject, "source-gcs-object", "_vfs-link/metadata.json", "legacy metadata GCS object")
	flags.Int64Var(&o.sourceGeneration, "source-gcs-generation", 0, "specific legacy object generation; 0 pins the latest generation")
	flags.StringVar(&o.sourceTreeDriver, "source-tree-driver", "", "distributed tree source: local or gcs")
	flags.StringVar(&o.sourceTreeRoot, "source-tree-local-root", "./data/metadata", "local distributed tree root")
	flags.StringVar(&o.sourceTreeBucket, "source-tree-gcs-bucket", "", "distributed tree GCS bucket")
	flags.StringVar(&o.sourceTreePrefix, "source-tree-prefix", "_vfs-link-v2", "existing distributed tree prefix: _vfs-link or _vfs-link-v2")
	flags.StringVar(&o.targetDriver, "target-driver", "local", "tree metadata target: local or gcs")
	flags.StringVar(&o.targetRoot, "target-local-root", "./data/metadata", "local tree metadata root")
	flags.StringVar(&o.targetBucket, "target-gcs-bucket", "", "GCS tree metadata bucket")
	flags.StringVar(&o.targetPrefix, "target-prefix", "_vfs-link-v3", "relative-path tree metadata prefix; must be _vfs-link-v3")
	flags.BoolVar(&o.dryRun, "dry-run", false, "decode and validate without writing")
	flags.BoolVar(&o.assumeYes, "yes", false, "confirm writing the tree target")
	flags.DurationVar(&o.timeout, "timeout", 24*time.Hour, "migration timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	sourceModes := 0
	for _, selected := range []bool{o.sourceFile != "", o.sourceBucket != "", o.sourceTreeDriver != ""} {
		if selected {
			sourceModes++
		}
	}
	if sourceModes != 1 {
		return errors.New("set exactly one source: --source-file, --source-gcs-bucket, or --source-tree-driver")
	}
	if !o.dryRun && !o.assumeYes {
		return errors.New("pass --yes after reviewing the target, or use --dry-run")
	}
	if err := validateTargetOptions(o); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	importSnapshot, err := loadImportSnapshot(ctx, o, output)
	if err != nil {
		return err
	}
	importSnapshot, err = canonicalizeImportSnapshot(importSnapshot)
	if err != nil {
		return fmt.Errorf("canonicalize relative logical paths: %w", err)
	}
	preflight, err := db.ValidateTreeImport(o.targetPrefix, importSnapshot)
	if err != nil {
		return fmt.Errorf("tree import preflight: %w", err)
	}
	fmt.Fprintf(output, "tree preflight: %+v; entities shares=%d activeLocks=%d activeUploads=%d\n",
		preflight, len(importSnapshot.Shares), len(importSnapshot.DAVLocks), len(importSnapshot.Uploads))
	if o.dryRun {
		fmt.Fprintln(output, "dry-run complete; target was not modified")
		return nil
	}

	target, err := openTreeTarget(ctx, o)
	if err != nil {
		return err
	}
	defer target.Close()
	if err := target.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure target schema: %w", err)
	}
	existing, err := target.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("inspect target: %w", err)
	}
	if len(existing) != 0 {
		return fmt.Errorf("target is not empty: found %d active records", len(existing))
	}
	validation, err := db.BulkImportTree(ctx, target, importSnapshot)
	if err != nil {
		return fmt.Errorf("bulk import tree: %w", err)
	}
	fmt.Fprintf(output, "target validation: %+v\n", validation)
	aggregates, err := validateRootAggregates(ctx, target)
	if err != nil {
		return fmt.Errorf("validate target aggregates: %w", err)
	}
	fmt.Fprintf(output, "target root aggregates: files=%d directories=%d bytes=%d (matches stats.json)\n",
		aggregates.Files, aggregates.Directories, aggregates.Bytes)
	fmt.Fprintln(output, "migration complete; keep the source metadata prefix and legacy metadata.json only as offline rollback backups")
	return nil
}

func loadImportSnapshot(ctx context.Context, o options, output io.Writer) (db.TreeImportSnapshot, error) {
	if o.sourceTreeDriver != "" {
		source, err := openTreeSource(ctx, o)
		if err != nil {
			return db.TreeImportSnapshot{}, err
		}
		defer source.Close()
		snapshot, err := db.ExportTreeSnapshot(ctx, source)
		if err != nil {
			return db.TreeImportSnapshot{}, fmt.Errorf("export distributed tree source: %w", err)
		}
		printImportSnapshotSummary(output, "distributed-tree", snapshot)
		return snapshot, nil
	}

	reader, generation, closeSource, err := openLegacySource(ctx, o)
	if err != nil {
		return db.TreeImportSnapshot{}, err
	}
	defer closeSource()
	digest := sha256.New()
	legacy, err := decodeLegacy(io.TeeReader(reader, digest))
	if err != nil {
		return db.TreeImportSnapshot{}, err
	}
	summary, err := validateLegacy(legacy)
	if err != nil {
		return db.TreeImportSnapshot{}, fmt.Errorf("validate source: %w", err)
	}
	sourceSHA256 := hex.EncodeToString(digest.Sum(nil))
	fmt.Fprintf(output, "source legacy generation=%d sha256=%s files=%d directories=%d bytes=%d ids=%d..%d nextFileId=%d shares=%d locks=%d uploads=%d\n",
		generation, sourceSHA256, summary.Files, summary.Directories, summary.Bytes, summary.MinID, summary.MaxID, legacy.NextFileID,
		summary.Shares, summary.DAVLocks, summary.Uploads)
	snapshot, _, _ := makeImportSnapshot(legacy, sourceSHA256, generation, time.Now())
	return snapshot, nil
}

func openTreeSource(ctx context.Context, o options) (db.Store, error) {
	if o.sourceTreePrefix != "_vfs-link" && o.sourceTreePrefix != "_vfs-link-v2" {
		return nil, errors.New("--source-tree-prefix must be _vfs-link or _vfs-link-v2")
	}
	switch strings.ToLower(strings.TrimSpace(o.sourceTreeDriver)) {
	case "local":
		if strings.TrimSpace(o.sourceTreeRoot) == "" {
			return nil, errors.New("--source-tree-local-root is required when --source-tree-driver=local")
		}
		return db.NewTreeLocal(o.sourceTreeRoot, o.sourceTreePrefix)
	case "gcs":
		if strings.TrimSpace(o.sourceTreeBucket) == "" {
			return nil, errors.New("--source-tree-gcs-bucket is required when --source-tree-driver=gcs")
		}
		return db.NewTreeGCS(ctx, o.sourceTreeBucket, o.sourceTreePrefix)
	default:
		return nil, fmt.Errorf("unsupported source tree driver %q", o.sourceTreeDriver)
	}
}

func printImportSnapshotSummary(output io.Writer, source string, snapshot db.TreeImportSnapshot) {
	canonical, canonicalErr := canonicalizeImportSnapshot(snapshot)
	validation, _ := db.ValidateTreeImport("_vfs-link-v3", canonical)
	if canonicalErr != nil {
		fmt.Fprintf(output, "source canonicalization error: %v\n", canonicalErr)
	}
	fmt.Fprintf(output, "source %s sha256=%s active=%d trash=%d files=%d directories=%d bytes=%d nextFileId=%d shares=%d locks=%d uploads=%d\n",
		source, snapshot.SourceSHA256, validation.Active, validation.Trash, validation.Files, validation.Directories,
		validation.Bytes, snapshot.NextFileID, len(snapshot.Shares), len(snapshot.DAVLocks), len(snapshot.Uploads))
}

func openLegacySource(ctx context.Context, o options) (io.Reader, int64, func(), error) {
	if o.sourceFile != "" {
		file, err := os.Open(o.sourceFile)
		if err != nil {
			return nil, 0, func() {}, fmt.Errorf("open legacy metadata file: %w", err)
		}
		return file, 0, func() { _ = file.Close() }, nil
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, 0, func() {}, fmt.Errorf("create source GCS client: %w", err)
	}
	object := client.Bucket(o.sourceBucket).Object(strings.TrimLeft(o.sourceObject, "/"))
	generation := o.sourceGeneration
	if generation == 0 {
		attrs, err := object.Attrs(ctx)
		if err != nil {
			_ = client.Close()
			return nil, 0, func() {}, fmt.Errorf("read source GCS attributes: %w", err)
		}
		generation = attrs.Generation
	}
	reader, err := object.Generation(generation).NewReader(ctx)
	if err != nil {
		_ = client.Close()
		return nil, 0, func() {}, fmt.Errorf("read source GCS generation %d: %w", generation, err)
	}
	return reader, generation, func() { _ = reader.Close(); _ = client.Close() }, nil
}

func openTreeTarget(ctx context.Context, o options) (db.Store, error) {
	switch strings.ToLower(strings.TrimSpace(o.targetDriver)) {
	case "local":
		return db.NewTreeLocal(o.targetRoot, o.targetPrefix)
	case "gcs":
		if strings.TrimSpace(o.targetBucket) == "" {
			return nil, errors.New("--target-gcs-bucket is required when --target-driver=gcs")
		}
		return db.NewTreeGCS(ctx, o.targetBucket, o.targetPrefix)
	default:
		return nil, fmt.Errorf("unsupported target driver %q", o.targetDriver)
	}
}

func validateTargetOptions(o options) error {
	if o.targetPrefix != "_vfs-link-v3" {
		return errors.New("--target-prefix must be the reserved _vfs-link-v3 prefix")
	}
	switch strings.ToLower(strings.TrimSpace(o.targetDriver)) {
	case "local":
		if strings.TrimSpace(o.targetRoot) == "" {
			return errors.New("--target-local-root is required when --target-driver=local")
		}
	case "gcs":
		if strings.TrimSpace(o.targetBucket) == "" {
			return errors.New("--target-gcs-bucket is required when --target-driver=gcs")
		}
	default:
		return fmt.Errorf("unsupported target driver %q", o.targetDriver)
	}
	return nil
}

func validateRootAggregates(ctx context.Context, store db.Store) (db.FolderSummary, error) {
	provider, ok := store.(db.MetadataStatsProvider)
	if !ok {
		return db.FolderSummary{}, fmt.Errorf("metadata store does not expose aggregate stats")
	}
	stats, err := provider.MetadataStats(ctx)
	if err != nil {
		return db.FolderSummary{}, fmt.Errorf("read metadata stats: %w", err)
	}
	page, err := store.ListDirectChildren(ctx, "", db.DirectChildrenOptions{Limit: 1})
	if err != nil {
		return db.FolderSummary{}, fmt.Errorf("read root folder summary: %w", err)
	}
	want := db.FolderSummary{Files: stats.LogicalFiles, Directories: stats.LogicalDirs, Bytes: stats.LogicalBytes}
	if page.FolderSummary != want {
		return page.FolderSummary, fmt.Errorf("root folder summary mismatch: got %+v want %+v", page.FolderSummary, want)
	}
	return page.FolderSummary, nil
}

func makeImportSnapshot(snapshot legacySnapshot, sha256 string, generation int64, now time.Time) (db.TreeImportSnapshot, int, int) {
	result := db.TreeImportSnapshot{
		Records:          snapshot.Files,
		Shares:           snapshot.Shares,
		NextFileID:       snapshot.NextFileID,
		SourceSHA256:     sha256,
		SourceGeneration: generation,
	}
	for _, lock := range snapshot.DAVLocks {
		if !lock.ExpiresAt.After(now) {
			continue
		}
		result.DAVLocks = append(result.DAVLocks, lock)
	}
	for _, upload := range snapshot.Uploads {
		if !upload.ExpiresAt.After(now) {
			continue
		}
		result.Uploads = append(result.Uploads, upload)
	}
	return result, len(result.DAVLocks), len(result.Uploads)
}
