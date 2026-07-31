package db

import (
	"context"
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
}

func (b *measuredTreeBackend) Get(ctx context.Context, key string) (treeObject, bool, error) {
	b.mu.Lock()
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
