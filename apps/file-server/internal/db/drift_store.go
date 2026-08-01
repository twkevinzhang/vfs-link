package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrDriftStateConflict = errors.New("drift state changed concurrently")

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
