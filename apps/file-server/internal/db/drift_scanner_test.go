package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

type measuredTreeBackend struct {
	mu         sync.Mutex
	objects    map[string]treeObject
	activeGets int
	maxGets    int
	delay      time.Duration
	gets       map[string]int
}

func (b *measuredTreeBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	b.mu.Lock()
	if b.gets != nil {
		b.gets[key]++
	}
	b.activeGets++
	if b.activeGets > b.maxGets {
		b.maxGets = b.activeGets
	}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.activeGets--; b.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return treeObject{}, false, ctx.Err()
	case <-time.After(b.delay):
	}
	o, ok := b.objects[key]
	return o, ok, nil
}
func (b *measuredTreeBackend) Put(context.Context, string, []byte, *int64) (int64, error) {
	return 0, errors.New("not implemented")
}
func (b *measuredTreeBackend) Delete(context.Context, string, *int64) error {
	return errors.New("not implemented")
}
func (b *measuredTreeBackend) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range b.objects {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, nil
}
func (*measuredTreeBackend) Close() error { return nil }

func TestTreeDriftActionListSkipsDismissedObjectReads(t *testing.T) {
	base := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	backend := &measuredTreeBackend{objects: map[string]treeObject{}, gets: map[string]int{}}
	store := newTreeStore(backend, "count-actions")
	for index, id := range []string{"dismissed", "visible"} {
		record := DriftActionRecord{ID: id, PlanID: "plan", IdempotencyKey: id, Status: "completed", Checkpoint: "completed", CreatedAt: base.Add(time.Duration(index) * time.Minute), UpdatedAt: base}
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		backend.objects[store.driftActionKey(id)] = treeObject{Data: payload, Generation: int64(index + 1)}
	}
	backend.objects[store.driftActionDismissalKey("dismissed")] = treeObject{Data: []byte(`{"actionId":"dismissed"}`), Generation: 3}

	actions, err := store.ListDriftActions(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].ID != "visible" {
		t.Fatalf("actions = %+v, want visible only", actions)
	}
	backend.mu.Lock()
	dismissedGets := backend.gets[store.driftActionKey("dismissed")]
	visibleGets := backend.gets[store.driftActionKey("visible")]
	backend.mu.Unlock()
	if dismissedGets != 0 || visibleGets != 1 {
		t.Fatalf("action object GETs = dismissed %d, visible %d; want 0 and 1", dismissedGets, visibleGets)
	}
}

func TestTreeDriftScannerIsBoundedAndDeterministic(t *testing.T) {
	backend := &measuredTreeBackend{objects: map[string]treeObject{}, delay: time.Millisecond}
	store := newTreeStore(backend, "scan-test")
	for i := 99; i >= 0; i-- {
		record := FileRecord{ID: i + 1, LogicPath: fmt.Sprintf("file-%03d", i), PhysicalHash: fmt.Sprintf("object-%03d", i), Size: int64(i)}
		data, err := marshalTree(record)
		if err != nil {
			t.Fatal(err)
		}
		backend.objects[store.activeKey(record.LogicPath, false)] = treeObject{Data: data, Generation: int64(i + 1)}
	}
	active, trash, err := store.ScanDriftRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 100 || len(trash) != 0 {
		t.Fatalf("record counts = %d active, %d trash", len(active), len(trash))
	}
	for index, record := range active {
		want := fmt.Sprintf("file-%03d", index)
		if record.LogicPath != want {
			t.Fatalf("active[%d] = %q, want %q", index, record.LogicPath, want)
		}
	}
	if backend.maxGets > 24 {
		t.Fatalf("maximum concurrent reads = %d, want <= 24", backend.maxGets)
	}
	if backend.maxGets < 2 {
		t.Fatalf("maximum concurrent reads = %d, expected bounded parallelism", backend.maxGets)
	}
}

func TestTreeDriftScannerHonorsCancellation(t *testing.T) {
	backend := &measuredTreeBackend{objects: map[string]treeObject{}, delay: time.Second}
	store := newTreeStore(backend, "cancel-test")
	data, _ := marshalTree(FileRecord{LogicPath: "slow", PhysicalHash: "slow"})
	backend.objects[store.activeKey("slow", false)] = treeObject{Data: data, Generation: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := store.ScanDriftRecords(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanDriftRecords error = %v, want context.Canceled", err)
	}
}
