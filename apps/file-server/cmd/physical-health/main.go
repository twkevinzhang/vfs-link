package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"cloud.google.com/go/storage"
	"github.com/joho/godotenv"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const (
	storageDriverLocal = "local"
	storageDriverGCS   = "gcs"

	statusOK           = "ok"
	statusObjectMiss   = "object_missing"
	statusSizeMismatch = "size_mismatch"
	statusStorageError = "storage_error"
)

type storageConfig struct {
	Driver    string
	LocalRoot string
	GCSBucket string
}

func (config storageConfig) target() string {
	if config.Driver == storageDriverGCS {
		return "gs://" + config.GCSBucket
	}
	return config.LocalRoot
}

type healthRow struct {
	LogicPath      string
	PhysicalHash   string
	ExpectedSize   int64
	TopDir         string
	Status         string
	Class          string
	StorageDriver  string
	ObjectLocation string
	ObjectSize     int64
	Error          string
}

type groupStats struct {
	Files       int
	Directories int
	Bytes       int64
	Classes     map[string]int
}

type gcsResult struct {
	index int
	size  int64
	err   error
}

type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func main() {
	if err := run(); err != nil {
		var exitErr exitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.code)
		}
		fmt.Fprintln(os.Stderr, "physical-health:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		envFile         string
		databaseDriver  string
		databaseURL     string
		metadataDriver  string
		metadataRoot    string
		metadataBucket  string
		metadataPrefix  string
		storageDriver   string
		localRoot       string
		gcsBucket       string
		credentials     string
		prefix          string
		csvPath         string
		failOnBad       bool
		checkAggregates bool
		workers         int
		timeout         time.Duration
	)

	flag.StringVar(&envFile, "env-file", ".env", "env file to load before reading database and storage settings")
	flag.StringVar(&databaseDriver, "db-driver", "", "metadata driver (postgres or json); defaults to DB_DRIVER or postgres")
	flag.StringVar(&databaseURL, "database-url", "", "PostgreSQL connection string; defaults to DATABASE_URL")
	flag.StringVar(&metadataDriver, "metadata-driver", "", "JSON tree storage driver; defaults to METADATA_STORAGE_DRIVER or local")
	flag.StringVar(&metadataRoot, "metadata-local-root", "", "local JSON tree root; defaults to METADATA_LOCAL_ROOT or ./data/metadata")
	flag.StringVar(&metadataBucket, "metadata-gcs-bucket", "", "JSON tree GCS bucket; defaults to METADATA_GCS_BUCKET")
	flag.StringVar(&metadataPrefix, "metadata-prefix", "", "JSON tree prefix; defaults to METADATA_PREFIX or _vfs-link")
	flag.StringVar(&storageDriver, "storage-driver", "", "active storage driver (local or gcs); defaults to STORAGE_DRIVER or local")
	flag.StringVar(&localRoot, "local-root", "", "local object root; defaults to LOCAL_STORAGE_ROOT or ./data/objects")
	flag.StringVar(&gcsBucket, "gcs-bucket", "", "active GCS bucket; defaults to GCS_BUCKET")
	flag.StringVar(&credentials, "google-credentials", "", "service account JSON path; defaults to GOOGLE_APPLICATION_CREDENTIALS")
	flag.StringVar(&prefix, "prefix", "/", "logical path prefix to inspect")
	flag.StringVar(&csvPath, "csv", "", "optional CSV report path")
	flag.BoolVar(&failOnBad, "fail-on-unhealthy", false, "exit with status 2 when any file is unhealthy")
	flag.BoolVar(&checkAggregates, "check-metadata-aggregates", false, "verify root folder summary and metadata stats against active records")
	flag.IntVar(&workers, "workers", 8, "concurrent GCS metadata checks")
	flag.DurationVar(&timeout, "timeout", 30*time.Minute, "overall scan timeout")
	flag.Parse()

	if envFile != "" {
		_ = godotenv.Load(envFile)
	}

	config, err := resolveStorageConfig(storageDriver, localRoot, gcsBucket)
	if err != nil {
		return err
	}
	if credentials != "" {
		if err := os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials); err != nil {
			return err
		}
	}
	if workers < 1 {
		workers = 1
	}
	prefix = cleanLogicPath(prefix)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	store, err := openMetadataStore(ctx, databaseDriver, databaseURL, metadataDriver, metadataRoot, metadataBucket, metadataPrefix)
	if err != nil {
		return err
	}
	defer store.Close()

	records, err := store.ListAll(ctx)
	if err != nil {
		return err
	}
	if checkAggregates {
		summary, aggregateErr := validateMetadataAggregates(ctx, store, records)
		if aggregateErr != nil {
			return aggregateErr
		}
		fmt.Printf("metadata aggregates: ok files=%d directories=%d bytes=%d\n", summary.Files, summary.Directories, summary.Bytes)
	}

	rows := make([]healthRow, 0)
	groups := map[string]*groupStats{}
	for _, record := range records {
		if !inScope(record.LogicPath, prefix) {
			continue
		}

		topDir := topDirectory(record.LogicPath)
		group := ensureGroup(groups, topDir)
		if record.IsDirectory {
			group.Directories++
			continue
		}

		row := healthRow{
			LogicPath:      record.LogicPath,
			PhysicalHash:   record.PhysicalHash,
			ExpectedSize:   record.Size,
			TopDir:         topDir,
			StorageDriver:  config.Driver,
			ObjectLocation: objectLocation(config, record.PhysicalHash),
		}
		group.Files++
		group.Bytes += record.Size
		rows = append(rows, row)
	}

	switch config.Driver {
	case storageDriverLocal:
		for i := range rows {
			checkLocal(&rows[i])
		}
	case storageDriverGCS:
		checkGCS(ctx, rows, config.GCSBucket, workers)
	}

	for i := range rows {
		if rows[i].Status == "" {
			rows[i].Status = statusStorageError
			rows[i].Error = "storage check did not produce a result"
		}
		rows[i].Class = classify(rows[i].Status)
		group := ensureGroup(groups, rows[i].TopDir)
		group.Classes[rows[i].Class]++
	}

	if csvPath != "" {
		if err := writeCSV(csvPath, rows); err != nil {
			return err
		}
	}

	printReport(prefix, config, rows, groups, csvPath)
	if failOnBad && countClass(rows, "unhealthy") > 0 {
		return exitCodeError{code: 2}
	}
	return nil
}

func openMetadataStore(ctx context.Context, driver string, databaseURL string, metadataDriver string, metadataRoot string, metadataBucket string, metadataPrefix string) (db.Store, error) {
	driver = strings.ToLower(firstNonEmpty(driver, os.Getenv("DB_DRIVER"), "postgres"))
	switch driver {
	case "postgres":
		databaseURL = firstNonEmpty(databaseURL, os.Getenv("DATABASE_URL"))
		if databaseURL == "" {
			return nil, errors.New("DATABASE_URL is required when DB_DRIVER=postgres")
		}
		return db.NewPostgres(ctx, databaseURL)
	case "json":
		metadataDriver = strings.ToLower(firstNonEmpty(metadataDriver, os.Getenv("METADATA_STORAGE_DRIVER"), storageDriverLocal))
		metadataPrefix = firstNonEmpty(metadataPrefix, os.Getenv("METADATA_PREFIX"), "_vfs-link-v3")
		if metadataPrefix != "_vfs-link" && metadataPrefix != "_vfs-link-v2" && metadataPrefix != "_vfs-link-v3" && metadataPrefix != "_vfs-link-v4" {
			return nil, errors.New("METADATA_PREFIX must be _vfs-link, _vfs-link-v2, _vfs-link-v3, or _vfs-link-v4")
		}
		switch metadataDriver {
		case storageDriverLocal:
			metadataRoot = firstNonEmpty(metadataRoot, os.Getenv("METADATA_LOCAL_ROOT"), "./data/metadata")
			if metadataPrefix == "_vfs-link-v4" {
				return db.NewTreeLocalV4(metadataRoot, metadataPrefix, db.TreeV4Options{ShardCount: 64, ReducerInterval: 2 * time.Second, MutationMode: "scoped"})
			}
			return db.NewTreeLocal(metadataRoot, metadataPrefix)
		case storageDriverGCS:
			metadataBucket = firstNonEmpty(metadataBucket, os.Getenv("METADATA_GCS_BUCKET"))
			if metadataBucket == "" {
				return nil, errors.New("METADATA_GCS_BUCKET is required when METADATA_STORAGE_DRIVER=gcs")
			}
			if metadataPrefix == "_vfs-link-v4" {
				return db.NewTreeGCSV4(ctx, metadataBucket, metadataPrefix, db.TreeV4Options{ShardCount: 64, ReducerInterval: 2 * time.Second, MutationMode: "scoped"})
			}
			return db.NewTreeGCS(ctx, metadataBucket, metadataPrefix)
		default:
			return nil, fmt.Errorf("unsupported METADATA_STORAGE_DRIVER %q", metadataDriver)
		}
	default:
		return nil, fmt.Errorf("unsupported DB_DRIVER %q: expected postgres or json", driver)
	}
}

func validateMetadataAggregates(ctx context.Context, store db.Store, records []db.FileRecord) (db.FolderSummary, error) {
	provider, ok := store.(db.MetadataStatsProvider)
	if !ok {
		return db.FolderSummary{}, errors.New("metadata store does not expose aggregate stats")
	}
	var expected db.MetadataStats
	physical := make(map[string]int64)
	for _, record := range records {
		if record.IsDirectory {
			expected.LogicalDirs++
			continue
		}
		expected.LogicalFiles++
		expected.LogicalBytes += record.Size
		physical[record.PhysicalHash] = record.Size
	}
	for key, size := range physical {
		if key == "" {
			continue
		}
		expected.PhysicalObjects++
		expected.PhysicalBytes += size
	}
	actual, err := provider.MetadataStats(ctx)
	if err != nil {
		return db.FolderSummary{}, fmt.Errorf("read metadata stats: %w", err)
	}
	if actual.LogicalFiles != expected.LogicalFiles || actual.LogicalDirs != expected.LogicalDirs || actual.LogicalBytes != expected.LogicalBytes ||
		actual.PhysicalObjects != expected.PhysicalObjects || actual.PhysicalBytes != expected.PhysicalBytes {
		return db.FolderSummary{}, fmt.Errorf("metadata stats mismatch: got files=%d dirs=%d bytes=%d objects=%d objectBytes=%d want files=%d dirs=%d bytes=%d objects=%d objectBytes=%d",
			actual.LogicalFiles, actual.LogicalDirs, actual.LogicalBytes, actual.PhysicalObjects, actual.PhysicalBytes,
			expected.LogicalFiles, expected.LogicalDirs, expected.LogicalBytes, expected.PhysicalObjects, expected.PhysicalBytes)
	}
	page, err := store.ListDirectChildren(ctx, "", db.DirectChildrenOptions{Limit: 1})
	if err != nil {
		return db.FolderSummary{}, fmt.Errorf("read root folder summary: %w", err)
	}
	wantSummary := db.FolderSummary{Files: expected.LogicalFiles, Directories: expected.LogicalDirs, Bytes: expected.LogicalBytes}
	if page.FolderSummary != wantSummary {
		return page.FolderSummary, fmt.Errorf("root folder summary mismatch: got %+v want %+v", page.FolderSummary, wantSummary)
	}
	return page.FolderSummary, nil
}

func resolveStorageConfig(driver string, localRoot string, gcsBucket string) (storageConfig, error) {
	driver = strings.ToLower(firstNonEmpty(driver, os.Getenv("STORAGE_DRIVER"), storageDriverLocal))
	config := storageConfig{Driver: driver}

	switch driver {
	case storageDriverLocal:
		root := firstNonEmpty(localRoot, os.Getenv("LOCAL_STORAGE_ROOT"), "./data/objects")
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return storageConfig{}, fmt.Errorf("resolve local root: %w", err)
		}
		config.LocalRoot = rootAbs
	case storageDriverGCS:
		config.GCSBucket = firstNonEmpty(gcsBucket, os.Getenv("GCS_BUCKET"))
		if config.GCSBucket == "" {
			return storageConfig{}, errors.New("GCS_BUCKET is required when STORAGE_DRIVER=gcs")
		}
	default:
		return storageConfig{}, fmt.Errorf("unsupported STORAGE_DRIVER %q: expected local or gcs", driver)
	}

	return config, nil
}

func objectLocation(config storageConfig, physicalHash string) string {
	if config.Driver == storageDriverGCS {
		return "gs://" + config.GCSBucket + "/" + cleanObjectName(physicalHash)
	}
	return localObjectPath(config.LocalRoot, physicalHash)
}

func checkLocal(row *healthRow) {
	info, err := os.Stat(row.ObjectLocation)
	if err == nil {
		row.ObjectSize = info.Size()
		if info.IsDir() {
			row.Status = statusStorageError
			row.Error = "object path is a directory"
			return
		}
		setSizeStatus(row)
		return
	}
	if os.IsNotExist(err) {
		row.Status = statusObjectMiss
		return
	}
	row.Status = statusStorageError
	row.Error = err.Error()
}

func checkGCS(ctx context.Context, rows []healthRow, bucket string, workers int) {
	if len(rows) == 0 {
		return
	}

	client, err := storage.NewClient(ctx)
	if err != nil {
		markStorageFailure(rows, fmt.Errorf("create GCS client: %w", err))
		return
	}
	defer client.Close()

	bucketHandle := client.Bucket(bucket)
	jobs := make(chan int)
	results := make(chan gcsResult, len(rows))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				attrs, err := bucketHandle.Object(cleanObjectName(rows[index].PhysicalHash)).Attrs(ctx)
				results <- gcsResult{index: index, size: objectSize(attrs), err: err}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index := range rows {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		row := &rows[result.index]
		if result.err != nil {
			if errors.Is(result.err, storage.ErrObjectNotExist) {
				row.Status = statusObjectMiss
			} else {
				row.Status = statusStorageError
				row.Error = result.err.Error()
			}
			continue
		}
		row.ObjectSize = result.size
		setSizeStatus(row)
	}
}

func objectSize(attrs *storage.ObjectAttrs) int64 {
	if attrs == nil {
		return 0
	}
	return attrs.Size
}

func setSizeStatus(row *healthRow) {
	if row.ObjectSize == row.ExpectedSize {
		row.Status = statusOK
		return
	}
	row.Status = statusSizeMismatch
	row.Error = fmt.Sprintf("DB size %d != object size %d", row.ExpectedSize, row.ObjectSize)
}

func markStorageFailure(rows []healthRow, err error) {
	for i := range rows {
		rows[i].Status = statusStorageError
		rows[i].Error = err.Error()
	}
}

func classify(status string) string {
	if status == statusOK {
		return "healthy"
	}
	return "unhealthy"
}

func writeCSV(filePath string, rows []healthRow) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)

	header := []string{"logicPath", "expectedSize", "physicalHash", "topDir", "status", "class", "storageDriver", "objectLocation", "objectSize", "error"}
	if err := writer.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{
			row.LogicPath,
			fmt.Sprintf("%d", row.ExpectedSize),
			row.PhysicalHash,
			row.TopDir,
			row.Status,
			row.Class,
			row.StorageDriver,
			row.ObjectLocation,
			fmt.Sprintf("%d", row.ObjectSize),
			row.Error,
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func printReport(prefix string, config storageConfig, rows []healthRow, groups map[string]*groupStats, csvPath string) {
	var totalBytes int64
	statusCounts := map[string]int{}
	classCounts := map[string]int{}
	for _, row := range rows {
		totalBytes += row.ExpectedSize
		statusCounts[row.Status]++
		classCounts[row.Class]++
	}

	fmt.Println("Physical object health check")
	fmt.Printf("Scope: %s\n", prefix)
	fmt.Printf("Files: %d (%s)\n", len(rows), formatBytes(totalBytes))
	fmt.Printf("Storage driver: %s\n", config.Driver)
	fmt.Printf("Storage target: %s\n", config.target())
	fmt.Println()

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CLASS\tCOUNT")
	for _, className := range []string{"healthy", "unhealthy"} {
		fmt.Fprintf(writer, "%s\t%d\n", className, classCounts[className])
	}
	writer.Flush()
	fmt.Println()

	writer = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCOUNT")
	for _, status := range sortedKeys(statusCounts) {
		fmt.Fprintf(writer, "%s\t%d\n", status, statusCounts[status])
	}
	writer.Flush()
	fmt.Println()

	writer = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "TOP_DIR\tFILES\tDIRS\tBYTES\tHEALTHY\tUNHEALTHY")
	for _, topDir := range sortedKeys(groups) {
		group := groups[topDir]
		fmt.Fprintf(
			writer,
			"%s\t%d\t%d\t%s\t%d\t%d\n",
			topDir,
			group.Files,
			group.Directories,
			formatBytes(group.Bytes),
			group.Classes["healthy"],
			group.Classes["unhealthy"],
		)
	}
	writer.Flush()
	if csvPath != "" {
		fmt.Printf("\nCSV report: %s\n", csvPath)
	}
}

func countClass(rows []healthRow, className string) int {
	total := 0
	for _, row := range rows {
		if row.Class == className {
			total++
		}
	}
	return total
}

func ensureGroup(groups map[string]*groupStats, topDir string) *groupStats {
	group := groups[topDir]
	if group == nil {
		group = &groupStats{Classes: map[string]int{}}
		groups[topDir] = group
	}
	return group
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cleanLogicPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(value, "/")), "/")
}

func inScope(logicPath string, prefix string) bool {
	if prefix == "" {
		return true
	}
	return logicPath == prefix || strings.HasPrefix(logicPath, prefix+"/")
}

func topDirectory(logicPath string) string {
	trimmed := strings.Trim(logicPath, "/")
	if trimmed == "" {
		return "/"
	}
	parts := strings.SplitN(trimmed, "/", 2)
	return parts[0]
}

func localObjectPath(root string, physicalHash string) string {
	cleaned := filepath.Clean("/" + strings.TrimPrefix(physicalHash, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	return filepath.Join(root, cleaned)
}

func cleanObjectName(physicalHash string) string {
	return strings.TrimLeft(strings.TrimSpace(physicalHash), "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}
