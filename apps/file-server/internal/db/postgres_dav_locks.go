package db

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const davLockAdvisoryKey int64 = 0x564653444156

func (s *PostgresStore) CreateDAVLock(ctx context.Context, record DAVLockRecord) (DAVLockRecord, error) {
	record.Token = strings.TrimSpace(record.Token)
	record.Path = cleanDAVPath(record.Path)
	if record.Token == "" {
		return DAVLockRecord{}, fmt.Errorf("DAV lock token is required")
	}
	if record.Depth != 0 && record.Depth != -1 {
		return DAVLockRecord{}, fmt.Errorf("DAV lock depth must be 0 or -1")
	}
	if record.ExpiresAt.IsZero() {
		return DAVLockRecord{}, fmt.Errorf("DAV lock expiry is required")
	}
	if !record.ExpiresAt.After(time.Now()) {
		return DAVLockRecord{}, fmt.Errorf("DAV lock expiry must be in the future")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DAVLockRecord{}, err
	}
	defer tx.Rollback(ctx)
	if err := acquireDAVLockAdvisoryLock(ctx, tx); err != nil {
		return DAVLockRecord{}, err
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE "expiresAt" <= now()
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`); err != nil {
		return DAVLockRecord{}, err
	}

	active, err := queryDAVLocks(ctx, tx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
FOR UPDATE
`)
	if err != nil {
		return DAVLockRecord{}, err
	}
	for _, existing := range active {
		if existing.Token == record.Token || davLocksConflict(existing, record) {
			return DAVLockRecord{}, ErrDAVLockConflict
		}
	}

	created, err := scanDAVLock(tx.QueryRow(ctx, `
INSERT INTO "DAVLock" (token, path, owner, depth, "expiresAt")
VALUES ($1, $2, $3, $4, $5)
RETURNING token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
`, record.Token, record.Path, record.Owner, record.Depth, record.ExpiresAt))
	if err != nil {
		return DAVLockRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DAVLockRecord{}, err
	}
	return created, nil
}

func (s *PostgresStore) FindDAVLock(ctx context.Context, token string) (DAVLockRecord, bool, error) {
	record, err := scanDAVLock(s.pool.QueryRow(ctx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE token = $1 AND "expiresAt" > now()
`, strings.TrimSpace(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return DAVLockRecord{}, false, nil
	}
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	return record, true, nil
}

func (s *PostgresStore) ListActiveDAVLocks(ctx context.Context, lockPath string) ([]DAVLockRecord, error) {
	lockPath = cleanDAVPath(lockPath)
	records, err := queryDAVLocks(ctx, s.pool, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
ORDER BY path, token
`)
	if err != nil {
		return nil, err
	}
	filtered := records[:0]
	for _, record := range records {
		if davLockCoversPath(record, lockPath) || davPathIsAncestor(lockPath, record.Path) {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (s *PostgresStore) RefreshDAVLock(ctx context.Context, token string, expiresAt time.Time) (DAVLockRecord, bool, error) {
	if expiresAt.IsZero() {
		return DAVLockRecord{}, false, fmt.Errorf("DAV lock expiry is required")
	}
	if !expiresAt.After(time.Now()) {
		return DAVLockRecord{}, false, fmt.Errorf("DAV lock expiry must be in the future")
	}
	record, err := scanDAVLock(s.pool.QueryRow(ctx, `
UPDATE "DAVLock"
SET "expiresAt" = $2
WHERE token = $1 AND "expiresAt" > now()
RETURNING token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
`, strings.TrimSpace(token), expiresAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return DAVLockRecord{}, false, nil
	}
	if err != nil {
		return DAVLockRecord{}, false, err
	}
	return record, true, nil
}

func (s *PostgresStore) DeleteDAVLock(ctx context.Context, token string) (bool, error) {
	result, err := s.pool.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE token = $1
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`, strings.TrimSpace(token))
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *PostgresStore) CleanupExpiredDAVLocks(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
DELETE FROM "DAVLock"
WHERE "expiresAt" <= now()
  AND ("heldUntil" IS NULL OR "heldUntil" <= now())
`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (s *PostgresStore) ClaimDAVLocks(ctx context.Context, paths []string, tokens []string, claimID string, until time.Time) (bool, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return false, fmt.Errorf("DAV lock claim ID is required")
	}
	if until.IsZero() {
		return false, fmt.Errorf("DAV lock claim expiry is required")
	}
	if !until.After(time.Now()) {
		return false, fmt.Errorf("DAV lock claim expiry must be in the future")
	}
	cleanPaths := make([]string, 0, len(paths))
	for _, lockPath := range paths {
		if strings.TrimSpace(lockPath) == "" {
			continue
		}
		cleanPaths = append(cleanPaths, cleanDAVPath(lockPath))
	}
	providedTokens := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		providedTokens[strings.TrimSpace(token)] = struct{}{}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err := acquireDAVLockAdvisoryLock(ctx, tx); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = NULL, "heldUntil" = NULL
WHERE "heldUntil" <= now()
`); err != nil {
		return false, err
	}

	active, err := queryDAVLocks(ctx, tx, `
SELECT token, path, owner, depth, "expiresAt", "createdAt", "heldBy", "heldUntil"
FROM "DAVLock"
WHERE "expiresAt" > now() OR "heldUntil" > now()
FOR UPDATE
`)
	if err != nil {
		return false, err
	}
	matched, claimable := claimableDAVLocks(active, cleanPaths, providedTokens, claimID)
	if !claimable {
		return false, nil
	}

	matchedTokens := make([]string, 0, len(matched))
	for token := range matched {
		matchedTokens = append(matchedTokens, token)
	}
	if len(matchedTokens) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = $1, "heldUntil" = $2
WHERE token = ANY($3)
`, claimID, until, matchedTokens); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func claimableDAVLocks(active []DAVLockRecord, paths []string, providedTokens map[string]struct{}, claimID string) (map[string]struct{}, bool) {
	matched := make(map[string]struct{})
	for _, record := range active {
		coversRequestedPath := false
		for _, lockPath := range paths {
			if davLockCoversPath(record, lockPath) {
				coversRequestedPath = true
				break
			}
		}
		if !coversRequestedPath {
			continue
		}
		if _, ok := providedTokens[record.Token]; !ok {
			return nil, false
		}
		if record.HeldBy != nil && *record.HeldBy != claimID {
			return nil, false
		}
		matched[record.Token] = struct{}{}
	}
	return matched, true
}

func (s *PostgresStore) ReleaseDAVLockClaim(ctx context.Context, claimID string) error {
	_, err := s.pool.Exec(ctx, `
UPDATE "DAVLock"
SET "heldBy" = NULL, "heldUntil" = NULL
WHERE "heldBy" = $1
`, strings.TrimSpace(claimID))
	return err
}

type davLockQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryDAVLocks(ctx context.Context, querier davLockQuerier, query string, args ...any) ([]DAVLockRecord, error) {
	rows, err := querier.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []DAVLockRecord
	for rows.Next() {
		record, err := scanDAVLock(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func acquireDAVLockAdvisoryLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, davLockAdvisoryKey)
	return err
}

func cleanDAVPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return pathpkg.Clean(value)
}

func davLocksConflict(first, second DAVLockRecord) bool {
	if first.Path == second.Path {
		return true
	}
	if first.Depth == -1 && davPathIsAncestor(first.Path, second.Path) {
		return true
	}
	return second.Depth == -1 && davPathIsAncestor(second.Path, first.Path)
}

func davLockCoversPath(record DAVLockRecord, value string) bool {
	value = cleanDAVPath(value)
	return record.Path == value || (record.Depth == -1 && davPathIsAncestor(record.Path, value))
}

func davPathIsAncestor(ancestor, descendant string) bool {
	ancestor = cleanDAVPath(ancestor)
	descendant = cleanDAVPath(descendant)
	if ancestor == descendant {
		return false
	}
	if ancestor == "/" {
		return strings.HasPrefix(descendant, "/")
	}
	return strings.HasPrefix(descendant, ancestor+"/")
}
