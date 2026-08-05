package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrDriftStateConflict     = errors.New("drift state changed concurrently")
	ErrDriftActionNotTerminal = errors.New("only completed or failed drift actions can be dismissed")
)

type DriftPlanRecord struct {
	ID          string          `json:"id"`
	Fingerprint string          `json:"fingerprint"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type DriftActionRecord struct {
	ID             string          `json:"id"`
	PlanID         string          `json:"planId"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Status         string          `json:"status"`
	Checkpoint     string          `json:"checkpoint"`
	Payload        json.RawMessage `json:"payload"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type DriftActionDismissalRecord struct {
	ActionID    string    `json:"actionId"`
	DismissedAt time.Time `json:"dismissedAt"`
}

// DriftStateStore is intentionally additive to Store. Existing metadata
// consumers do not need drift privileges, while both production backends can
// persist restartable actions.
type DriftStateStore interface {
	SaveDriftSnapshot(context.Context, json.RawMessage) error
	LoadDriftSnapshot(context.Context) (json.RawMessage, bool, error)
	CreateDriftPlan(context.Context, DriftPlanRecord) (DriftPlanRecord, error)
	FindDriftPlan(context.Context, string) (DriftPlanRecord, bool, error)
	CreateDriftAction(context.Context, DriftActionRecord) (DriftActionRecord, error)
	FindDriftAction(context.Context, string) (DriftActionRecord, bool, error)
	ListDriftActions(context.Context, int, int) ([]DriftActionRecord, error)
	DismissDriftAction(context.Context, string, time.Time) (bool, error)
	RestoreDriftAction(context.Context, string) error
	UpdateDriftAction(context.Context, DriftActionRecord, int64) (DriftActionRecord, error)
}

func (s *PostgresStore) SaveDriftSnapshot(ctx context.Context, payload json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO "DriftSnapshot" (id,payload,"updatedAt") VALUES (1,$1,now()) ON CONFLICT (id) DO UPDATE SET payload=EXCLUDED.payload,"updatedAt"=EXCLUDED."updatedAt"`, payload)
	return err
}

func (s *PostgresStore) LoadDriftSnapshot(ctx context.Context) (json.RawMessage, bool, error) {
	var payload json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT payload FROM "DriftSnapshot" WHERE id=1`).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return payload, err == nil, err
}

func AsDriftStateStore(store Store) (DriftStateStore, error) {
	p, ok := store.(DriftStateStore)
	if !ok {
		return nil, fmt.Errorf("metadata backend %T does not support drift state", store)
	}
	return p, nil
}

func scanDriftPlan(row pgx.Row) (DriftPlanRecord, error) {
	var r DriftPlanRecord
	err := row.Scan(&r.ID, &r.Fingerprint, &r.Payload, &r.CreatedAt)
	return r, err
}

func (s *PostgresStore) CreateDriftPlan(ctx context.Context, r DriftPlanRecord) (DriftPlanRecord, error) {
	err := s.pool.QueryRow(ctx, `
INSERT INTO "DriftPlan" (id, fingerprint, payload, "createdAt") VALUES ($1,$2,$3,$4)
ON CONFLICT (id) DO UPDATE SET id=EXCLUDED.id
RETURNING id, fingerprint, payload, "createdAt"`, r.ID, r.Fingerprint, r.Payload, r.CreatedAt).
		Scan(&r.ID, &r.Fingerprint, &r.Payload, &r.CreatedAt)
	return r, err
}

func (s *PostgresStore) FindDriftPlan(ctx context.Context, id string) (DriftPlanRecord, bool, error) {
	r, err := scanDriftPlan(s.pool.QueryRow(ctx, `SELECT id,fingerprint,payload,"createdAt" FROM "DriftPlan" WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DriftPlanRecord{}, false, nil
	}
	return r, err == nil, err
}

func scanDriftAction(row pgx.Row) (DriftActionRecord, error) {
	var r DriftActionRecord
	err := row.Scan(&r.ID, &r.PlanID, &r.IdempotencyKey, &r.Status, &r.Checkpoint, &r.Payload, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *PostgresStore) CreateDriftAction(ctx context.Context, r DriftActionRecord) (DriftActionRecord, error) {
	err := s.pool.QueryRow(ctx, `
INSERT INTO "DriftAction" (id,"planId","idempotencyKey",status,checkpoint,payload,version,"createdAt","updatedAt")
VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8)
ON CONFLICT (id) DO UPDATE SET id=EXCLUDED.id
RETURNING id,"planId","idempotencyKey",status,checkpoint,payload,version,"createdAt","updatedAt"`,
		r.ID, r.PlanID, r.IdempotencyKey, r.Status, r.Checkpoint, r.Payload, r.CreatedAt, r.UpdatedAt).
		Scan(&r.ID, &r.PlanID, &r.IdempotencyKey, &r.Status, &r.Checkpoint, &r.Payload, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *PostgresStore) FindDriftAction(ctx context.Context, id string) (DriftActionRecord, bool, error) {
	r, err := scanDriftAction(s.pool.QueryRow(ctx, `SELECT id,"planId","idempotencyKey",status,checkpoint,payload,version,"createdAt","updatedAt" FROM "DriftAction" WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DriftActionRecord{}, false, nil
	}
	return r, err == nil, err
}

func (s *PostgresStore) ListDriftActions(ctx context.Context, offset, limit int) ([]DriftActionRecord, error) {
	query := `
SELECT a.id,a."planId",a."idempotencyKey",a.status,a.checkpoint,a.payload,a.version,a."createdAt",a."updatedAt"
FROM "DriftAction" a
LEFT JOIN "DriftActionDismissal" d ON d."actionId"=a.id
WHERE d."actionId" IS NULL
ORDER BY a."createdAt" DESC,a.id DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT $1 OFFSET $2`
		args = []any{limit, offset}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]DriftActionRecord, 0, limit)
	for rows.Next() {
		record, scanErr := scanDriftAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) DismissDriftAction(ctx context.Context, id string, dismissedAt time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the action row so a concurrent retry cannot change a terminal action
	// to running between the status check and the dismissal insert.
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM "DriftAction" WHERE id=$1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "completed" && status != "failed" {
		return true, ErrDriftActionNotTerminal
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO "DriftActionDismissal" ("actionId","dismissedAt") VALUES ($1,$2)
ON CONFLICT ("actionId") DO NOTHING`, id, dismissedAt); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) RestoreDriftAction(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Use the same row lock as dismissal. If dismissal began while the action
	// was terminal, this waits for its tombstone to commit before deleting it.
	var actionID string
	err = tx.QueryRow(ctx, `SELECT id FROM "DriftAction" WHERE id=$1 FOR UPDATE`, id).Scan(&actionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM "DriftActionDismissal" WHERE "actionId"=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateDriftAction(ctx context.Context, r DriftActionRecord, expected int64) (DriftActionRecord, error) {
	err := s.pool.QueryRow(ctx, `UPDATE "DriftAction" SET status=$2,checkpoint=$3,payload=$4,version=version+1,"updatedAt"=$5 WHERE id=$1 AND version=$6 RETURNING id,"planId","idempotencyKey",status,checkpoint,payload,version,"createdAt","updatedAt"`,
		r.ID, r.Status, r.Checkpoint, r.Payload, r.UpdatedAt, expected).
		Scan(&r.ID, &r.PlanID, &r.IdempotencyKey, &r.Status, &r.Checkpoint, &r.Payload, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DriftActionRecord{}, ErrDriftStateConflict
	}
	return r, err
}

func (s *TreeStore) driftPlanKey(id string) string { return s.prefix + "/drift/plans/" + id + ".json" }
func (s *TreeStore) driftActionKey(id string) string {
	return s.prefix + "/drift/actions/" + id + ".json"
}
func (s *TreeStore) driftActionDismissalKey(id string) string {
	return s.prefix + "/drift/action-dismissals/" + id + ".json"
}
func (s *TreeStore) driftSnapshotKey() string { return s.prefix + "/drift/snapshots/current.json" }

func (s *TreeStore) SaveDriftSnapshot(ctx context.Context, payload json.RawMessage) error {
	key := s.driftSnapshotKey()
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		o, ok, err := s.objects.Get(ctx, key)
		if err != nil {
			return err
		}
		expected := int64(0)
		if ok {
			expected = o.Generation
		}
		if _, err = s.objects.Put(ctx, key, append(payload, '\n'), &expected); err == nil {
			return nil
		} else if !errors.Is(err, ErrMetadataConflict) {
			return err
		}
	}
	return ErrDriftStateConflict
}

func (s *TreeStore) LoadDriftSnapshot(ctx context.Context) (json.RawMessage, bool, error) {
	o, ok, err := s.objects.Get(ctx, s.driftSnapshotKey())
	return json.RawMessage(o.Data), ok, err
}

func (s *TreeStore) CreateDriftPlan(ctx context.Context, r DriftPlanRecord) (DriftPlanRecord, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	zero := int64(0)
	if _, err = s.objects.Put(ctx, s.driftPlanKey(r.ID), append(b, '\n'), &zero); errors.Is(err, ErrMetadataConflict) {
		existing, ok, getErr := s.FindDriftPlan(ctx, r.ID)
		if getErr != nil || !ok || existing.Fingerprint != r.Fingerprint {
			return r, ErrDriftStateConflict
		}
		return existing, nil
	}
	return r, err
}

func (s *TreeStore) FindDriftPlan(ctx context.Context, id string) (DriftPlanRecord, bool, error) {
	o, ok, err := s.objects.Get(ctx, s.driftPlanKey(id))
	if err != nil || !ok {
		return DriftPlanRecord{}, ok, err
	}
	var r DriftPlanRecord
	err = json.Unmarshal(o.Data, &r)
	return r, err == nil, err
}

func (s *TreeStore) CreateDriftAction(ctx context.Context, r DriftActionRecord) (DriftActionRecord, error) {
	r.Version = 1
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	zero := int64(0)
	generation, putErr := s.objects.Put(ctx, s.driftActionKey(r.ID), append(b, '\n'), &zero)
	if err = putErr; errors.Is(err, ErrMetadataConflict) {
		existing, ok, getErr := s.FindDriftAction(ctx, r.ID)
		if getErr != nil || !ok || existing.PlanID != r.PlanID || existing.IdempotencyKey != r.IdempotencyKey {
			return r, ErrDriftStateConflict
		}
		return existing, nil
	}
	r.Version = generation
	return r, err
}

func (s *TreeStore) FindDriftAction(ctx context.Context, id string) (DriftActionRecord, bool, error) {
	o, ok, err := s.objects.Get(ctx, s.driftActionKey(id))
	if err != nil || !ok {
		return DriftActionRecord{}, ok, err
	}
	var r DriftActionRecord
	if err = json.Unmarshal(o.Data, &r); err != nil {
		return r, false, err
	}
	// The backend generation is the authoritative CAS token. Keeping it in
	// Version makes the same contract work for JSON/GCS and PostgreSQL.
	r.Version = o.Generation
	return r, true, nil
}

func (s *TreeStore) ListDriftActions(ctx context.Context, offset, limit int) ([]DriftActionRecord, error) {
	actionPrefix := s.prefix + "/drift/actions/"
	dismissalPrefix := s.prefix + "/drift/action-dismissals/"
	keys, err := s.objects.List(ctx, actionPrefix)
	if err != nil {
		return nil, err
	}
	dismissalKeys, err := s.objects.List(ctx, dismissalPrefix)
	if err != nil {
		return nil, err
	}
	dismissed := make(map[string]struct{}, len(dismissalKeys))
	dismissalPrefix = strings.TrimPrefix(dismissalPrefix, "/")
	for _, key := range dismissalKeys {
		key = strings.TrimPrefix(key, "/")
		id := strings.TrimSuffix(strings.TrimPrefix(key, dismissalPrefix), ".json")
		dismissed[id] = struct{}{}
	}

	tasks := make(chan string)
	results := make(chan struct {
		record DriftActionRecord
		err    error
	})
	workerCount := 24
	if len(keys) < workerCount {
		workerCount = len(keys)
	}
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for key := range tasks {
				o, ok, readErr := s.objects.Get(ctx, key)
				if readErr != nil {
					results <- struct {
						record DriftActionRecord
						err    error
					}{err: readErr}
					continue
				}
				if !ok {
					continue
				}
				var record DriftActionRecord
				decodeErr := json.Unmarshal(o.Data, &record)
				record.Version = o.Generation
				results <- struct {
					record DriftActionRecord
					err    error
				}{record: record, err: decodeErr}
			}
		}()
	}
	go func() {
		defer close(tasks)
		normalizedActionPrefix := strings.TrimPrefix(actionPrefix, "/")
		for _, key := range keys {
			normalizedKey := strings.TrimPrefix(key, "/")
			id := strings.TrimSuffix(strings.TrimPrefix(normalizedKey, normalizedActionPrefix), ".json")
			if _, hidden := dismissed[id]; hidden {
				continue
			}
			select {
			case tasks <- key:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	records := make([]DriftActionRecord, 0, len(keys))
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		records = append(records, result.record)
	}
	if firstErr != nil {
		return nil, fmt.Errorf("read drift action: %w", firstErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.After(records[j].CreatedAt)
		}
		return records[i].ID > records[j].ID
	})
	if offset >= len(records) {
		return []DriftActionRecord{}, nil
	}
	if limit == 0 {
		return records, nil
	}
	records = records[offset:]
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *TreeStore) DismissDriftAction(ctx context.Context, id string, dismissedAt time.Time) (bool, error) {
	action, ok, err := s.FindDriftAction(ctx, id)
	if err != nil || !ok {
		return ok, err
	}
	if action.Status != "completed" && action.Status != "failed" {
		return true, ErrDriftActionNotTerminal
	}
	record := DriftActionDismissalRecord{ActionID: id, DismissedAt: dismissedAt}
	payload, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	zero := int64(0)
	_, err = s.objects.Put(ctx, s.driftActionDismissalKey(id), append(payload, '\n'), &zero)
	if errors.Is(err, ErrMetadataConflict) {
		err = nil
	}
	if err != nil {
		return false, err
	}
	live, found, err := s.FindDriftAction(ctx, id)
	if err != nil || !found {
		_ = s.RestoreDriftAction(ctx, id)
		return found, err
	}
	if live.Status != "completed" && live.Status != "failed" {
		if cleanupErr := s.RestoreDriftAction(ctx, id); cleanupErr != nil {
			return true, fmt.Errorf("%w; remove raced dismissal: %v", ErrDriftActionNotTerminal, cleanupErr)
		}
		return true, ErrDriftActionNotTerminal
	}
	return true, nil
}

func (s *TreeStore) RestoreDriftAction(ctx context.Context, id string) error {
	return s.objects.Delete(ctx, s.driftActionDismissalKey(id), nil)
}

func (s *TreeStore) UpdateDriftAction(ctx context.Context, r DriftActionRecord, expected int64) (DriftActionRecord, error) {
	r.Version = expected
	b, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	generation, err := s.objects.Put(ctx, s.driftActionKey(r.ID), append(b, '\n'), &expected)
	if errors.Is(err, ErrMetadataConflict) {
		return r, ErrDriftStateConflict
	}
	r.Version = generation
	return r, err
}
