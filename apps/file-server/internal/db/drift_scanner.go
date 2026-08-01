package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// DriftRecordScanner returns all active and trash records needed to establish
// physical reference counts. TreeStore bounds concurrent metadata reads so a
// production refresh does not perform ~68k sequential GCS round trips.
type DriftRecordScanner interface {
	ScanDriftRecords(context.Context) (active []FileRecord, trash []FileRecord, err error)
}

func (s *PostgresStore) ScanDriftRecords(ctx context.Context) ([]FileRecord, []FileRecord, error) {
	active, err := s.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	trash, err := s.ListTrashRecords(ctx, nil)
	return active, trash, err
}

type driftTreeRead struct {
	record FileRecord
	trash  bool
	err    error
}

func (s *TreeStore) ScanDriftRecords(ctx context.Context) ([]FileRecord, []FileRecord, error) {
	activePrefix := s.prefix + "/tree/nodes/"
	trashPrefix := s.prefix + "/trash/"
	activeKeys, err := s.objects.List(ctx, activePrefix)
	if err != nil {
		return nil, nil, err
	}
	trashKeys, err := s.objects.List(ctx, trashPrefix)
	if err != nil {
		return nil, nil, err
	}
	type task struct {
		key   string
		trash bool
	}
	tasks := make(chan task)
	results := make(chan driftTreeRead)
	workerCount := 24
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for task := range tasks {
				o, ok, readErr := s.objects.Get(ctx, task.key)
				if readErr != nil {
					results <- driftTreeRead{err: readErr}
					continue
				}
				if !ok {
					continue
				}
				r, decodeErr := decodeTreeRecord(o)
				results <- driftTreeRead{record: r, trash: task.trash, err: decodeErr}
			}
		}()
	}
	go func() {
		defer close(tasks)
		for _, key := range activeKeys {
			select {
			case tasks <- task{key: key}:
			case <-ctx.Done():
				return
			}
		}
		for _, key := range trashKeys {
			if !strings.Contains(key, "/files/") && !strings.Contains(key, "/directories/") {
				continue
			}
			select {
			case tasks <- task{key: key, trash: true}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	active := make([]FileRecord, 0, len(activeKeys))
	trash := make([]FileRecord, 0)
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		if result.trash {
			trash = append(trash, result.record)
		} else {
			active = append(active, result.record)
		}
	}
	if firstErr != nil {
		return nil, nil, fmt.Errorf("read drift metadata: %w", firstErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(active, func(i, j int) bool { return active[i].LogicPath < active[j].LogicPath })
	sort.Slice(trash, func(i, j int) bool { return trash[i].LogicPath < trash[j].LogicPath })
	return active, trash, nil
}
