// metadata-v4-http-load is a bounded HTTP acceptance/load harness for the v4
// metadata namespace. It only mutates a unique vfs-link-acceptance/<run-id>
// subtree and always attempts trash + permanent-delete cleanup.
//
// Production defaults:
//
//	VFS_LOAD_BASE_URL=https://... \
//	VFS_LOAD_USERNAME=vfs_link \
//	VFS_LOAD_PASSWORD_FILE=/secure/path/password \
//	go run ./scripts/metadata-v4-http-load.go
//
// The password can instead be supplied through VFS_LOAD_PASSWORD. Neither
// credentials nor opaque GCS upload URLs are ever printed. Run the complete
// lifecycle against an in-process mock with:
//
//	go run ./scripts/metadata-v4-http-load.go -self-test
//
// Workload overrides are VFS_LOAD_CLIENTS, VFS_LOAD_RATE,
// VFS_LOAD_DURATION, VFS_LOAD_BURST_RATE, VFS_LOAD_BURST_DURATION,
// VFS_LOAD_RETRIES, VFS_LOAD_REQUEST_TIMEOUT, VFS_LOAD_OPERATION_TIMEOUT,
// VFS_LOAD_AGGREGATE_TIMEOUT, VFS_LOAD_AGGREGATE_POLL,
// VFS_LOAD_AGGREGATE_SAMPLES (minimum 100), VFS_LOAD_WORKING_SET,
// VFS_LOAD_SHARD_COUNT, and VFS_LOAD_PREFIX.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	baseURL          string
	username         string
	password         string
	clients          int
	rate             float64
	duration         time.Duration
	burstRate        float64
	burstDuration    time.Duration
	retries          int
	requestTimeout   time.Duration
	operationTimeout time.Duration
	aggregateTimeout time.Duration
	aggregatePoll    time.Duration
	aggregateSamples int
	workingSet       int
	shardCount       int
	prefix           string
	selfTest         bool
}

type apiClient struct {
	base             *url.URL
	http             *http.Client
	username         string
	password         string
	operationTimeout time.Duration
}

type fixture struct {
	parent       string
	currentPath  string
	currentName  string
	alternate    string
	payload      []byte
	physicalHash string
}

type entry struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Size         int64  `json:"size"`
	PhysicalHash string `json:"physicalHash"`
	TrashID      string `json:"trashId"`
}

type folderSummary struct {
	Files       int64 `json:"files"`
	Directories int64 `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

type filesResponse struct {
	Entries       []entry       `json:"entries"`
	FolderSummary folderSummary `json:"folderSummary"`
}

type uploadResponse struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	UploadURL   string            `json:"uploadUrl"`
	Headers     map[string]string `json:"headers"`
	CompleteURL string            `json:"completeUrl"`
}

type operationResponse struct {
	OperationID string  `json:"operationId"`
	Status      string  `json:"status"`
	Error       string  `json:"error"`
	Entries     []entry `json:"entries"`
}

type entriesResponse struct {
	Entries []entry `json:"entries"`
}

type statusResponse struct {
	Stats struct {
		FileCount      int   `json:"fileCount"`
		DirectoryCount int   `json:"directoryCount"`
		TotalBytes     int64 `json:"totalBytes"`
	} `json:"stats"`
}

type apiError struct {
	status int
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP status %d", e.status) }

type metrics struct {
	mu                  sync.Mutex
	latencies           []time.Duration
	attempts            int64
	successes           int64
	errors              int64
	retries             int64
	retryExhaustions    int64
	correctnessFailures int64
	started             time.Time
	ended               time.Time
}

type report struct {
	Name                string  `json:"name"`
	ElapsedSeconds      float64 `json:"elapsedSeconds"`
	Attempts            int64   `json:"attempts"`
	Successes           int64   `json:"successes"`
	Errors              int64   `json:"errors"`
	Retries             int64   `json:"retries"`
	RetryExhaustions    int64   `json:"retryExhaustions"`
	CorrectnessFailures int64   `json:"correctnessFailures"`
	P50Milliseconds     float64 `json:"p50Milliseconds"`
	P95Milliseconds     float64 `json:"p95Milliseconds"`
	P99Milliseconds     float64 `json:"p99Milliseconds"`
	SuccessfulRPS       float64 `json:"successfulRps"`
}

type phaseGate struct {
	Name                string  `json:"name"`
	ScheduledTarget     int64   `json:"scheduledTarget"`
	MinimumSuccesses    int64   `json:"minimumSuccesses"`
	ScheduledRPS        float64 `json:"scheduledRps"`
	MinimumRPS          float64 `json:"minimumRps"`
	ActualRPS           float64 `json:"actualRps"`
	UnexpectedErrorRate float64 `json:"unexpectedErrorRate"`
	P95Milliseconds     float64 `json:"p95Milliseconds"`
	P99Milliseconds     float64 `json:"p99Milliseconds"`
	Passed              bool    `json:"passed"`
}

type aggregateGate struct {
	Samples         int     `json:"samples"`
	P99Milliseconds float64 `json:"p99Milliseconds"`
	MaxMilliseconds float64 `json:"maxMilliseconds"`
	Passed          bool    `json:"passed"`
}

type contentionGate struct {
	Contenders      int  `json:"contenders"`
	Successes       int  `json:"successes"`
	ExpectedRejects int  `json:"expectedRejects"`
	FinalPathUnique bool `json:"finalPathUnique"`
	Passed          bool `json:"passed"`
}

type gateSummary struct {
	Steady    phaseGate     `json:"steady"`
	Burst     phaseGate     `json:"burst"`
	Aggregate aggregateGate `json:"aggregate"`
	Passed    bool          `json:"passed"`
}

func main() {
	selfTest := flag.Bool("self-test", false, "run a short full lifecycle against an in-process mock")
	flag.Parse()
	cfg, err := loadConfig(*selfTest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration failed:", err)
		os.Exit(2)
	}
	if err = run(context.Background(), cfg); err != nil {
		fmt.Fprintln(os.Stderr, "acceptance failed:", err)
		os.Exit(1)
	}
}

func loadConfig(selfTest bool) (config, error) {
	mock := selfTest || envBool("VFS_LOAD_MOCK")
	cfg := config{
		baseURL:          strings.TrimSpace(os.Getenv("VFS_LOAD_BASE_URL")),
		username:         os.Getenv("VFS_LOAD_USERNAME"),
		password:         os.Getenv("VFS_LOAD_PASSWORD"),
		clients:          envInt("VFS_LOAD_CLIENTS", 12),
		rate:             envFloat("VFS_LOAD_RATE", 2),
		duration:         envDuration("VFS_LOAD_DURATION", 30*time.Minute),
		burstRate:        envFloat("VFS_LOAD_BURST_RATE", 4),
		burstDuration:    envDuration("VFS_LOAD_BURST_DURATION", 5*time.Minute),
		retries:          envInt("VFS_LOAD_RETRIES", 3),
		requestTimeout:   envDuration("VFS_LOAD_REQUEST_TIMEOUT", 15*time.Second),
		operationTimeout: envDuration("VFS_LOAD_OPERATION_TIMEOUT", 5*time.Minute),
		aggregateTimeout: envDuration("VFS_LOAD_AGGREGATE_TIMEOUT", 30*time.Second),
		aggregatePoll:    envDuration("VFS_LOAD_AGGREGATE_POLL", 250*time.Millisecond),
		aggregateSamples: envInt("VFS_LOAD_AGGREGATE_SAMPLES", 100),
		workingSet:       envInt("VFS_LOAD_WORKING_SET", 4),
		shardCount:       envInt("VFS_LOAD_SHARD_COUNT", 64),
		prefix:           strings.Trim(strings.TrimSpace(envString("VFS_LOAD_PREFIX", "vfs-link-acceptance")), "/"),
		selfTest:         mock,
	}
	if file := strings.TrimSpace(os.Getenv("VFS_LOAD_PASSWORD_FILE")); file != "" {
		payload, err := os.ReadFile(file)
		if err != nil {
			return cfg, fmt.Errorf("read password file")
		}
		cfg.password = strings.TrimRight(string(payload), "\r\n")
	}
	if mock {
		if os.Getenv("VFS_LOAD_CLIENTS") == "" {
			cfg.clients = 2
		}
		if os.Getenv("VFS_LOAD_RATE") == "" {
			cfg.rate = 10
		}
		if os.Getenv("VFS_LOAD_DURATION") == "" {
			cfg.duration = 500 * time.Millisecond
		}
		if os.Getenv("VFS_LOAD_BURST_RATE") == "" {
			cfg.burstRate = 20
		}
		if os.Getenv("VFS_LOAD_BURST_DURATION") == "" {
			cfg.burstDuration = 300 * time.Millisecond
		}
		if os.Getenv("VFS_LOAD_AGGREGATE_TIMEOUT") == "" {
			cfg.aggregateTimeout = 2 * time.Second
		}
		if os.Getenv("VFS_LOAD_OPERATION_TIMEOUT") == "" {
			cfg.operationTimeout = 2 * time.Second
		}
		if os.Getenv("VFS_LOAD_AGGREGATE_POLL") == "" {
			cfg.aggregatePoll = 20 * time.Millisecond
		}
		if os.Getenv("VFS_LOAD_WORKING_SET") == "" {
			cfg.workingSet = 2
		}
	}
	if cfg.clients < 1 || cfg.clients > 128 || cfg.rate <= 0 || cfg.burstRate <= 0 || cfg.duration <= 0 || cfg.burstDuration <= 0 {
		return cfg, fmt.Errorf("clients, rates, and durations must be positive and clients <= 128")
	}
	if cfg.retries < 0 || cfg.retries > 20 || cfg.requestTimeout <= 0 || cfg.operationTimeout <= 0 || cfg.aggregateTimeout <= 0 || cfg.aggregatePoll <= 0 || cfg.aggregateSamples < 100 || cfg.aggregateSamples > 1000 {
		return cfg, fmt.Errorf("invalid retry or timeout setting")
	}
	if cfg.shardCount < 2 || cfg.shardCount > 256 || cfg.shardCount&(cfg.shardCount-1) != 0 || cfg.workingSet < 1 || cfg.workingSet*2 > cfg.shardCount {
		return cfg, fmt.Errorf("working set must fit distinct source/target pairs within a power-of-two shard count")
	}
	if cfg.prefix == "" || strings.Contains(cfg.prefix, "..") {
		return cfg, fmt.Errorf("VFS_LOAD_PREFIX must be a safe relative path")
	}
	if !mock && cfg.baseURL == "" {
		return cfg, fmt.Errorf("VFS_LOAD_BASE_URL is required")
	}
	if (cfg.username == "") != (cfg.password == "") {
		return cfg, fmt.Errorf("username and password must be supplied together")
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config) error {
	var mock *httptest.Server
	if cfg.selfTest {
		mock = newMockServer()
		defer mock.Close()
		cfg.baseURL = mock.URL
	}
	base, err := url.Parse(cfg.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return fmt.Errorf("invalid base URL")
	}
	client := &apiClient{
		base: base, username: cfg.username, password: cfg.password, operationTimeout: cfg.operationTimeout,
		http: &http.Client{Timeout: cfg.requestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}
	runID := time.Now().UTC().Format("20060102T150405Z") + "-" + randomHex(4)
	root := path.Join(cfg.prefix, runID)
	namePairs := distinctShardFixtureNames(cfg.workingSet, cfg.shardCount)
	clientFixtures := make([][]*fixture, cfg.clients)
	fixtures := make([]*fixture, 0, cfg.clients*cfg.workingSet)
	for clientIndex := range clientFixtures {
		parent := path.Join(root, fmt.Sprintf("client-%03d", clientIndex))
		for slot, pair := range namePairs {
			payload := []byte(fmt.Sprintf("vfs-load-%03d-%03d", clientIndex, slot))
			item := &fixture{parent: parent, currentName: pair[0], alternate: pair[1], currentPath: path.Join(parent, pair[0]), payload: payload}
			clientFixtures[clientIndex] = append(clientFixtures[clientIndex], item)
			fixtures = append(fixtures, item)
		}
	}
	cleanupNeeded := false
	defer func() {
		if cleanupNeeded {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*cfg.operationTimeout)
			defer cancel()
			_ = cleanup(cleanupCtx, client, root)
		}
	}()
	if err = createFixtures(ctx, client, clientFixtures, cfg.retries); err != nil {
		cleanupNeeded = true
		return fmt.Errorf("fixture setup: %w", err)
	}
	cleanupNeeded = true
	contention, err := runSamePathContention(ctx, client, clientFixtures[0][0], cfg.clients)
	if err != nil {
		return fmt.Errorf("same-path contention: %w", err)
	}
	contentionPayload, _ := json.Marshal(contention)
	fmt.Println(string(contentionPayload))
	expectedBytes := int64(0)
	for _, fixture := range fixtures {
		expectedBytes += int64(len(fixture.payload))
	}
	expected := folderSummary{Files: int64(len(fixtures)), Directories: int64(cfg.clients), Bytes: expectedBytes}
	initialConvergence, err := waitForAggregate(ctx, client, root, expected, cfg.aggregateTimeout, cfg.aggregatePoll)
	if err != nil {
		return fmt.Errorf("initial aggregate convergence: %w", err)
	}
	fmt.Printf("fixtures_ready clients=%d working_set=%d shards=%d aggregate_convergence_ms=%.1f\n", cfg.clients, cfg.workingSet, cfg.shardCount, milliseconds(initialConvergence))
	normal := runPhase(ctx, "steady", client, clientFixtures, cfg.rate, cfg.duration, cfg.retries)
	printReport(normal)
	burst := runPhase(ctx, "burst", client, clientFixtures, cfg.burstRate, cfg.burstDuration, cfg.retries)
	printReport(burst)
	convergenceSamples, err := sampleAggregateConvergence(ctx, client, root, expected, cfg.aggregateSamples, cfg.aggregateTimeout, cfg.aggregatePoll)
	if err != nil {
		return err
	}
	aggregateResult := evaluateAggregateGate(convergenceSamples)
	var finalStatus statusResponse
	if err = client.json(ctx, http.MethodGet, "/api/status", nil, &finalStatus, http.StatusOK); err != nil {
		return fmt.Errorf("global stats sanity check: %w", err)
	}
	if finalStatus.Stats.FileCount < 0 || finalStatus.Stats.DirectoryCount < 0 || finalStatus.Stats.TotalBytes < 0 {
		return fmt.Errorf("global stats sanity check returned a negative value")
	}
	steadyGate := evaluatePhaseGate(normal, cfg.clients, cfg.rate, cfg.duration)
	burstGate := evaluatePhaseGate(burst, cfg.clients, cfg.burstRate, cfg.burstDuration)
	gates := gateSummary{Steady: steadyGate, Burst: burstGate, Aggregate: aggregateResult}
	gates.Passed = steadyGate.Passed && burstGate.Passed && aggregateResult.Passed
	gatePayload, _ := json.Marshal(gates)
	fmt.Println(string(gatePayload))
	fmt.Println("global_stats_sanity=ok")
	cleanupCtx, cancelCleanup := context.WithTimeout(ctx, 2*cfg.operationTimeout)
	err = cleanup(cleanupCtx, client, root)
	cancelCleanup()
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	cleanupNeeded = false
	if !gates.Passed {
		fmt.Println("cleanup=completed acceptance=failed")
		return fmt.Errorf("one or more throughput, latency, error-rate, or aggregate gates failed")
	}
	fmt.Println("cleanup=completed acceptance=passed")
	return nil
}

func runSamePathContention(ctx context.Context, client *apiClient, f *fixture, contenders int) (contentionGate, error) {
	oldPath, oldName := f.currentPath, f.currentName
	newName := f.alternate
	newPath := path.Join(f.parent, newName)
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var operation operationResponse
			err := client.json(ctx, http.MethodPost, "/api/files/rename", map[string]any{"path": oldPath, "name": newName}, &operation, http.StatusOK, http.StatusAccepted)
			if err == nil && operation.OperationID != "" {
				err = client.awaitOperation(ctx, operation.OperationID)
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	unique, verifyErr := client.verifyRename(ctx, f.parent, oldPath, newPath, int64(len(f.payload)), f.physicalHash)
	gate := contentionGate{
		Contenders:      contenders,
		Successes:       successes,
		ExpectedRejects: contenders - successes,
		FinalPathUnique: unique,
	}
	gate.Passed = successes == 1 && unique
	if !gate.Passed {
		return gate, fmt.Errorf("successes=%d, want 1; unique final path=%t; verify error=%v", successes, unique, verifyErr)
	}
	f.currentPath, f.currentName, f.alternate = newPath, newName, oldName
	return gate, nil
}

func createFixtures(ctx context.Context, client *apiClient, clients [][]*fixture, maxRetries int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(clients))
	var wg sync.WaitGroup
	for _, fixtures := range clients {
		fixtures := fixtures
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, item := range fixtures {
				if err := createFixture(ctx, client, item, maxRetries); err != nil {
					errs <- err
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	return errors.Join(channelErrors(errs)...)
}

func createFixture(ctx context.Context, client *apiClient, item *fixture, maxRetries int) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := client.upload(ctx, item, false)
		if err == nil {
			return nil
		}
		// The response can be lost after upload completion. Reconcile the unique
		// fixture path before retrying creation with overwrite disabled.
		if listed, listErr := client.list(ctx, item.parent); listErr == nil {
			for _, candidate := range listed.Entries {
				if candidate.Path == item.currentPath && candidate.Size == int64(len(item.payload)) {
					item.physicalHash = candidate.PhysicalHash
					return nil
				}
			}
		}
		if attempt == maxRetries || !retryable(err) {
			return fmt.Errorf("create fixture after %d retries: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
		}
	}
	return errors.New("fixture retry loop exhausted")
}

func fixtureShard(name string, shards int) int {
	digest := sha256.Sum256([]byte(name))
	return int(binary.BigEndian.Uint32(digest[:4]) % uint32(shards))
}

func distinctShardFixtureNames(count, shards int) [][2]string {
	pairs := make([][2]string, 0, count)
	used := make(map[int]bool, count*2)
	for candidate := 0; len(pairs) < count; candidate++ {
		first := fmt.Sprintf("fixture-%04d-a.txt", candidate)
		second := fmt.Sprintf("fixture-%04d-b.txt", candidate)
		firstShard, secondShard := fixtureShard(first, shards), fixtureShard(second, shards)
		if firstShard == secondShard || used[firstShard] || used[secondShard] {
			continue
		}
		used[firstShard], used[secondShard] = true, true
		pairs = append(pairs, [2]string{first, second})
	}
	return pairs
}

func runPhase(parent context.Context, name string, client *apiClient, clients [][]*fixture, rate float64, duration time.Duration, retries int) report {
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	m := &metrics{started: time.Now()}
	interval := time.Duration(float64(time.Second) / rate)
	var wg sync.WaitGroup
	for _, fixtures := range clients {
		fixtureInterval := interval * time.Duration(len(fixtures))
		for fixtureIndex, item := range fixtures {
			item := item
			initialDelay := interval * time.Duration(fixtureIndex+1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				timer := time.NewTimer(initialDelay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				mutate := func() {
					latency, retryCount, exhausted, correctness := mutateOnce(parent, client, item, retries)
					m.record(latency, retryCount, exhausted, correctness)
				}
				mutate()
				ticker := time.NewTicker(fixtureInterval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						// Each fixture owns distinct source/target shards. Scheduling one
						// worker per fixture lets a client sustain its aggregate rate without
						// issuing concurrent renames against the same logical path.
						mutate()
					}
				}
			}()
		}
	}
	wg.Wait()
	m.ended = time.Now()
	var auditWG sync.WaitGroup
	for _, fixtures := range clients {
		fixtures := fixtures
		auditWG.Add(1)
		go func() {
			defer auditWG.Done()
			m.recordCorrectnessFailures(auditFixtures(parent, client, fixtures))
		}()
	}
	auditWG.Wait()
	return m.report(name)
}

func auditFixtures(ctx context.Context, client *apiClient, fixtures []*fixture) int64 {
	if len(fixtures) == 0 {
		return 0
	}
	listed, err := client.list(ctx, fixtures[0].parent)
	if err != nil {
		return int64(len(fixtures))
	}
	entries := make(map[string]entry, len(listed.Entries))
	for _, item := range listed.Entries {
		entries[item.Path] = item
	}
	failures := int64(0)
	for _, item := range fixtures {
		current, found := entries[item.currentPath]
		alternatePath := path.Join(item.parent, item.alternate)
		_, alternateFound := entries[alternatePath]
		if !found || alternateFound || current.Size != int64(len(item.payload)) || (item.physicalHash != "" && current.PhysicalHash != item.physicalHash) {
			failures++
		}
	}
	return failures
}

func mutateOnce(ctx context.Context, client *apiClient, f *fixture, maxRetries int) (time.Duration, int, bool, bool) {
	started := time.Now()
	oldPath, oldName := f.currentPath, f.currentName
	newName := f.alternate
	newPath := path.Join(f.parent, newName)
	correctnessFailure := false
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var operation operationResponse
		err := client.json(ctx, http.MethodPost, "/api/files/rename", map[string]any{"path": oldPath, "name": newName}, &operation, http.StatusOK, http.StatusAccepted)
		if err == nil && operation.OperationID != "" {
			err = client.awaitOperation(ctx, operation.OperationID)
		}
		if err == nil {
			// A successful rename response is emitted only after the path commit.
			// The next alternating mutation implicitly validates this state, and a
			// full per-client audit after the phase independently verifies every
			// final path without charging directory-list latency to the mutation.
			f.currentPath, f.currentName, f.alternate = newPath, newName, oldName
			return time.Since(started), attempt, false, correctnessFailure
		} else {
			// A response can be lost after the rename commit. Reconcile before
			// retrying a non-idempotent operation.
			if ok, _ := client.verifyRename(ctx, f.parent, oldPath, newPath, int64(len(f.payload)), f.physicalHash); ok {
				f.currentPath, f.currentName, f.alternate = newPath, newName, oldName
				return time.Since(started), attempt, false, correctnessFailure
			}
		}
		if attempt == maxRetries || !retryable(err) {
			return time.Since(started), attempt, true, correctnessFailure
		}
		select {
		case <-ctx.Done():
			return time.Since(started), attempt, true, correctnessFailure
		case <-time.After(time.Duration(1<<attempt) * 25 * time.Millisecond):
		}
	}
	return time.Since(started), maxRetries, true, correctnessFailure
}

func (m *metrics) record(latency time.Duration, retries int, exhausted, correctness bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	m.retries += int64(retries)
	m.latencies = append(m.latencies, latency)
	if exhausted {
		m.errors++
		m.retryExhaustions++
	} else {
		m.successes++
	}
	if correctness {
		m.correctnessFailures++
	}
}

func (m *metrics) recordCorrectnessFailures(failures int64) {
	m.mu.Lock()
	m.correctnessFailures += failures
	m.mu.Unlock()
}

func (m *metrics) report(name string) report {
	m.mu.Lock()
	defer m.mu.Unlock()
	sort.Slice(m.latencies, func(i, j int) bool { return m.latencies[i] < m.latencies[j] })
	elapsed := m.ended.Sub(m.started).Seconds()
	result := report{Name: name, ElapsedSeconds: elapsed, Attempts: m.attempts, Successes: m.successes, Errors: m.errors, Retries: m.retries, RetryExhaustions: m.retryExhaustions, CorrectnessFailures: m.correctnessFailures}
	result.P50Milliseconds = milliseconds(percentile(m.latencies, .50))
	result.P95Milliseconds = milliseconds(percentile(m.latencies, .95))
	result.P99Milliseconds = milliseconds(percentile(m.latencies, .99))
	if elapsed > 0 {
		result.SuccessfulRPS = float64(result.Successes) / elapsed
	}
	return result
}

func printReport(result report) {
	payload, _ := json.Marshal(result)
	fmt.Println(string(payload))
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1)*p + .5)
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func milliseconds(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }

func (c *apiClient) upload(ctx context.Context, f *fixture, overwrite bool) error {
	var created uploadResponse
	err := c.json(ctx, http.MethodPost, "/api/uploads", map[string]any{"path": f.currentPath, "size": len(f.payload), "contentType": "text/plain", "overwrite": overwrite}, &created, http.StatusCreated)
	if err != nil {
		return err
	}
	if created.ID == "" || created.UploadURL == "" || created.CompleteURL == "" {
		return errors.New("upload session response is incomplete")
	}
	putErr := c.putUpload(ctx, created.UploadURL, created.Headers, f.payload)
	var completed uploadResponse
	if err = c.json(ctx, http.MethodPost, created.CompleteURL, nil, &completed, http.StatusOK); err != nil {
		if putErr != nil {
			return errors.Join(putErr, fmt.Errorf("reconcile upload completion: %w", err))
		}
		return err
	}
	// A resumable PUT response can be lost after GCS committed the object. The
	// completion endpoint verifies the declared size and conditionally commits
	// the unique acceptance path, so a successful completion reconciles that
	// ambiguous transport result without opening a second upload session.
	if completed.Status != "complete" {
		return errors.New("upload did not reach complete business status")
	}
	listed, err := c.list(ctx, f.parent)
	if err != nil {
		return err
	}
	for _, item := range listed.Entries {
		if item.Path == f.currentPath && item.Size == int64(len(f.payload)) {
			f.physicalHash = item.PhysicalHash
			return nil
		}
	}
	return errors.New("completed upload is not immediately visible")
}

func (c *apiClient) putUpload(ctx context.Context, target string, headers map[string]string, payload []byte) error {
	resolved, err := c.resolve(target)
	if err != nil {
		return errors.New("invalid opaque upload target")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, resolved.String(), bytes.NewReader(payload))
	if err != nil {
		return errors.New("create upload request")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if sameOrigin(c.base, resolved) && c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("execute upload request")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &apiError{status: response.StatusCode}
	}
	return nil
}

func (c *apiClient) json(ctx context.Context, method, target string, input, output any, expected ...int) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	resolved, err := c.resolve(target)
	if err != nil {
		return errors.New("invalid API target")
	}
	request, err := http.NewRequestWithContext(ctx, method, resolved.String(), body)
	if err != nil {
		return errors.New("create API request")
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("execute API request")
	}
	defer response.Body.Close()
	allowed := false
	for _, status := range expected {
		allowed = allowed || response.StatusCode == status
	}
	if !allowed {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return &apiError{status: response.StatusCode}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return errors.New("decode API response")
	}
	return nil
}

func (c *apiClient) resolve(target string) (*url.URL, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	return c.base.ResolveReference(parsed), nil
}

func (c *apiClient) list(ctx context.Context, directory string) (filesResponse, error) {
	query := url.Values{"path": {directory}, "limit": {"1000"}}
	var response filesResponse
	err := c.json(ctx, http.MethodGet, "/api/files?"+query.Encode(), nil, &response, http.StatusOK)
	return response, err
}

func (c *apiClient) verifyRename(ctx context.Context, directory, oldPath, newPath string, size int64, physicalHash string) (bool, error) {
	response, err := c.list(ctx, directory)
	if err != nil {
		return false, err
	}
	oldFound, newFound := false, false
	for _, item := range response.Entries {
		oldFound = oldFound || item.Path == oldPath
		if item.Path == newPath && item.Size == size && (physicalHash == "" || item.PhysicalHash == physicalHash) {
			newFound = true
		}
	}
	return !oldFound && newFound, nil
}

func (c *apiClient) awaitOperation(ctx context.Context, id string) error {
	_, err := c.awaitOperationResult(ctx, id)
	return err
}

func (c *apiClient) awaitOperationResult(ctx context.Context, id string) (operationResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	defer cancel()
	consecutiveErrors := 0
	for {
		var operation operationResponse
		if err := c.json(ctx, http.MethodGet, "/api/operations/"+url.PathEscape(id), nil, &operation, http.StatusOK); err != nil {
			consecutiveErrors++
			if !retryable(err) || consecutiveErrors > 3 {
				return operationResponse{}, fmt.Errorf("poll operation after %d consecutive errors: %w", consecutiveErrors, err)
			}
			select {
			case <-ctx.Done():
				return operationResponse{}, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		consecutiveErrors = 0
		switch operation.Status {
		case "completed":
			return operation, nil
		case "failed", "aborted":
			return operation, errors.New("operation reached unsuccessful terminal status")
		}
		select {
		case <-ctx.Done():
			return operationResponse{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func waitForAggregate(ctx context.Context, client *apiClient, root string, expected folderSummary, timeout, poll time.Duration) (time.Duration, error) {
	started := time.Now()
	deadline := started.Add(timeout)
	for {
		response, err := client.list(ctx, root)
		if err == nil && response.FolderSummary == expected {
			return time.Since(started), nil
		}
		if time.Now().After(deadline) {
			return time.Since(started), fmt.Errorf("folder summary did not converge to files=%d directories=%d bytes=%d", expected.Files, expected.Directories, expected.Bytes)
		}
		select {
		case <-ctx.Done():
			return time.Since(started), ctx.Err()
		case <-time.After(poll):
		}
	}
}

func sampleAggregateConvergence(ctx context.Context, client *apiClient, root string, baseline folderSummary, samples int, timeout, poll time.Duration) ([]time.Duration, error) {
	probe := &fixture{parent: root, currentName: "aggregate-probe.txt", currentPath: path.Join(root, "aggregate-probe.txt"), payload: []byte("aggregate-probe")}
	expected := baseline
	latencies := make([]time.Duration, 0, samples)
	probeLive := false
	defer func() {
		if probeLive {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*client.operationTimeout)
			defer cancel()
			_ = trashAndDelete(cleanupCtx, client, probe.currentPath)
		}
	}()
	for index := 0; index < samples; index++ {
		if !probeLive {
			probe.physicalHash = ""
			if err := client.upload(ctx, probe, false); err != nil {
				return nil, fmt.Errorf("aggregate sample %d create: %w", index+1, err)
			}
			probeLive = true
			expected.Files++
			expected.Bytes += int64(len(probe.payload))
		} else {
			if err := trashAndDelete(ctx, client, probe.currentPath); err != nil {
				return nil, fmt.Errorf("aggregate sample %d delete: %w", index+1, err)
			}
			probeLive = false
			expected.Files--
			expected.Bytes -= int64(len(probe.payload))
		}
		latency, err := waitForAggregate(ctx, client, root, expected, timeout, poll)
		if err != nil {
			return nil, fmt.Errorf("aggregate sample %d: %w", index+1, err)
		}
		latencies = append(latencies, latency)
	}
	return latencies, nil
}

func evaluatePhaseGate(result report, clients int, rate float64, duration time.Duration) phaseGate {
	interval := time.Duration(float64(time.Second) / rate)
	perClient := int64(0)
	if duration > 0 && interval > 0 {
		perClient = int64((duration - 1) / interval)
	}
	target := int64(clients) * perClient
	minimumSuccesses := int64(float64(target)*0.99 + 0.999999)
	scheduledRPS := 0.0
	if duration > 0 {
		scheduledRPS = float64(target) / duration.Seconds()
	}
	errorRate := 0.0
	if result.Attempts > 0 {
		errorRate = float64(result.Errors) / float64(result.Attempts)
	} else {
		errorRate = 1
	}
	gate := phaseGate{
		Name: result.Name, ScheduledTarget: target, MinimumSuccesses: minimumSuccesses,
		ScheduledRPS: scheduledRPS, MinimumRPS: scheduledRPS * 0.99,
		UnexpectedErrorRate: errorRate, P95Milliseconds: result.P95Milliseconds, P99Milliseconds: result.P99Milliseconds,
	}
	if duration > 0 {
		// Gate RPS uses the bounded scheduling window. The phase report separately
		// shows wall-clock RPS including deliberate in-flight request draining.
		gate.ActualRPS = float64(result.Successes) / duration.Seconds()
	}
	gate.Passed = target > 0 && result.Successes >= minimumSuccesses && gate.ActualRPS >= gate.MinimumRPS &&
		errorRate < 0.001 && result.RetryExhaustions == 0 && result.CorrectnessFailures == 0 &&
		result.P95Milliseconds < 1000 && result.P99Milliseconds < 2000
	return gate
}

func evaluateAggregateGate(samples []time.Duration) aggregateGate {
	values := append([]time.Duration(nil), samples...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := aggregateGate{Samples: len(values), P99Milliseconds: milliseconds(percentile(values, .99))}
	if len(values) > 0 {
		result.MaxMilliseconds = milliseconds(values[len(values)-1])
	}
	result.Passed = len(values) >= 100 && result.P99Milliseconds <= 5000 && result.MaxMilliseconds <= 30000
	return result
}

func cleanup(ctx context.Context, client *apiClient, root string) error {
	return trashAndDelete(ctx, client, root)
}

func trashAndDelete(ctx context.Context, client *apiClient, target string) error {
	var trashed operationResponse
	if err := client.json(ctx, http.MethodPost, "/api/files/trash", map[string]any{"paths": []string{target}}, &trashed, http.StatusOK, http.StatusAccepted); err != nil {
		var status *apiError
		if errors.As(err, &status) && status.status == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("trash target: %w", err)
	}
	if trashed.OperationID != "" {
		completed, err := client.awaitOperationResult(ctx, trashed.OperationID)
		if err != nil {
			return fmt.Errorf("await trash operation: %w", err)
		}
		trashed = completed
	}
	trashID := ""
	for _, item := range trashed.Entries {
		if item.Path == target && item.TrashID != "" {
			trashID = item.TrashID
			break
		}
	}
	if trashID == "" {
		var trash entriesResponse
		if err := client.json(ctx, http.MethodGet, "/api/trash", nil, &trash, http.StatusOK); err != nil {
			return fmt.Errorf("list trash for target id: %w", err)
		}
		for _, item := range trash.Entries {
			if item.Path == target && item.TrashID != "" {
				trashID = item.TrashID
				break
			}
		}
	}
	if trashID == "" {
		return errors.New("cleanup trash record not found")
	}
	var deleted operationResponse
	if err := client.json(ctx, http.MethodPost, "/api/trash/delete", map[string]any{"trashIds": []string{trashID}}, &deleted, http.StatusOK, http.StatusAccepted); err != nil {
		return fmt.Errorf("delete trash target: %w", err)
	}
	if deleted.OperationID != "" {
		if err := client.awaitOperation(ctx, deleted.OperationID); err != nil {
			return fmt.Errorf("await delete-trash operation: %w", err)
		}
	}
	return nil
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	var status *apiError
	if errors.As(err, &status) {
		return status.status == http.StatusTooManyRequests || status.status == http.StatusConflict || status.status >= 500
	}
	return true
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func channelErrors(ch <-chan error) []error {
	var result []error
	for err := range ch {
		result = append(result, err)
	}
	return result
}

func randomHex(bytesCount int) string {
	payload := make([]byte, bytesCount)
	if _, err := rand.Read(payload); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(payload)
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envFloat(name string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return -1
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envBool(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value == "1" || value == "true" || value == "yes"
}

// mockState implements the exact endpoints used by the harness. It introduces
// a small aggregate delay so self-test exercises convergence polling.
type mockState struct {
	mu             sync.Mutex
	files          map[string][]byte
	hashes         map[string]string
	trash          map[string]string
	aggregateReady time.Time
	uploads        map[string]struct {
		path string
		size int
		data []byte
	}
	sequence atomic.Int64
}

func newMockServer() *httptest.Server {
	state := &mockState{files: map[string][]byte{}, hashes: map[string]string{}, trash: map[string]string{}, uploads: make(map[string]struct {
		path string
		size int
		data []byte
	})}
	return httptest.NewServer(http.HandlerFunc(state.serveHTTP))
}

func (m *mockState) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		m.mu.Lock()
		files, total := len(m.files), int64(0)
		dirs := map[string]bool{}
		for name, data := range m.files {
			total += int64(len(data))
			for dir := path.Dir(name); dir != "." && dir != ""; dir = path.Dir(dir) {
				dirs[dir] = true
			}
		}
		m.mu.Unlock()
		writeMock(w, http.StatusOK, map[string]any{"stats": map[string]any{"fileCount": files, "directoryCount": len(dirs), "totalBytes": total}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/uploads":
		var input struct {
			Path string `json:"path"`
			Size int    `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		id := strconv.FormatInt(m.sequence.Add(1), 10)
		m.mu.Lock()
		m.uploads[id] = struct {
			path string
			size int
			data []byte
		}{path: input.Path, size: input.Size}
		m.mu.Unlock()
		writeMock(w, http.StatusCreated, uploadResponse{ID: id, Status: "pending", UploadURL: "/api/uploads/" + id + "/content", CompleteURL: "/api/uploads/" + id + "/complete"})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/content"):
		id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[2]
		data, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		upload := m.uploads[id]
		upload.data = data
		m.uploads[id] = upload
		m.mu.Unlock()
		writeMock(w, http.StatusOK, map[string]any{"status": "uploaded"})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete"):
		id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[2]
		m.mu.Lock()
		upload := m.uploads[id]
		m.files[upload.path] = upload.data
		m.hashes[upload.path] = "mock-" + id
		m.aggregateReady = time.Now().Add(40 * time.Millisecond)
		m.mu.Unlock()
		writeMock(w, http.StatusOK, uploadResponse{ID: id, Status: "complete"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/files/rename":
		var input struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		to := path.Join(path.Dir(input.Path), input.Name)
		m.mu.Lock()
		data, exists := m.files[input.Path]
		if exists {
			delete(m.files, input.Path)
			m.files[to] = data
			hash := m.hashes[input.Path]
			delete(m.hashes, input.Path)
			m.hashes[to] = hash
		}
		m.mu.Unlock()
		if !exists {
			writeMock(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeMock(w, http.StatusOK, entriesResponse{Entries: []entry{{Path: to, Name: input.Name, Kind: "file", Size: int64(len(data))}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/files":
		dir := r.URL.Query().Get("path")
		m.mu.Lock()
		var response filesResponse
		for name, data := range m.files {
			if path.Dir(name) == dir {
				response.Entries = append(response.Entries, entry{Name: path.Base(name), Path: name, Kind: "file", Size: int64(len(data)), PhysicalHash: m.hashes[name]})
			}
			if strings.HasPrefix(name, dir+"/") && time.Now().After(m.aggregateReady) {
				response.FolderSummary.Files++
				response.FolderSummary.Bytes += int64(len(data))
				child := strings.Split(strings.TrimPrefix(name, dir+"/"), "/")[0]
				if strings.Contains(strings.TrimPrefix(name, dir+"/"), "/") {
					_ = child
				}
			}
		}
		if time.Now().After(m.aggregateReady) {
			dirs := map[string]bool{}
			for name := range m.files {
				rel := strings.TrimPrefix(name, dir+"/")
				if rel != name && strings.Contains(rel, "/") {
					dirs[strings.Split(rel, "/")[0]] = true
				}
			}
			response.FolderSummary.Directories = int64(len(dirs))
		}
		m.mu.Unlock()
		writeMock(w, http.StatusOK, response)
	case r.Method == http.MethodPost && r.URL.Path == "/api/files/trash":
		var input struct {
			Paths []string `json:"paths"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		root := input.Paths[0]
		trashID := "trash-" + strconv.FormatInt(m.sequence.Add(1), 10)
		m.mu.Lock()
		m.trash[root] = trashID
		for name := range m.files {
			if name == root || strings.HasPrefix(name, root+"/") {
				delete(m.files, name)
				delete(m.hashes, name)
			}
		}
		m.aggregateReady = time.Now().Add(40 * time.Millisecond)
		m.mu.Unlock()
		writeMock(w, http.StatusOK, entriesResponse{Entries: []entry{{Path: root, TrashID: trashID}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/trash":
		m.mu.Lock()
		var response entriesResponse
		for name, id := range m.trash {
			response.Entries = append(response.Entries, entry{Path: name, TrashID: id})
		}
		m.mu.Unlock()
		writeMock(w, http.StatusOK, response)
	case r.Method == http.MethodPost && r.URL.Path == "/api/trash/delete":
		var input struct {
			TrashIDs []string `json:"trashIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		m.mu.Lock()
		for name, id := range m.trash {
			for _, requested := range input.TrashIDs {
				if id == requested {
					delete(m.trash, name)
				}
			}
		}
		m.mu.Unlock()
		writeMock(w, http.StatusOK, map[string]int{"deleted": 1})
	default:
		writeMock(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func writeMock(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
