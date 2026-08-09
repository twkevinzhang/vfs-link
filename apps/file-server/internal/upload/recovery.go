package upload

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RecoverDue advances at most batchSize durable completion or cleanup records.
// Each record is attempted once per scan; retryable failures are persisted by
// Complete/RetryCleanup and are reconsidered only by a later scan.
func (s *Service) RecoverDue(ctx context.Context, batchSize int) (RecoveryResult, error) {
	batchSize = boundedRecoveryBatch(batchSize)
	sessions, err := s.repository.ListDueRecoveries(ctx, s.now().UTC(), batchSize)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("list due upload recoveries: %w", err)
	}
	if len(sessions) > batchSize {
		sessions = sessions[:batchSize]
	}

	result := RecoveryResult{Scanned: len(sessions)}
	var failures []error
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if session.Status == StatusComplete && session.CompletionStatus == CompletionComplete && session.CleanupStatus == CleanupPending {
			_, recoverErr := s.RetryCleanup(ctx, session.ID)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			if classifyCleanupRecovery(&result, recoverErr) {
				failures = append(failures, fmt.Errorf("recover upload %s cleanup: %w", session.ID, recoverErr))
			}
			continue
		}

		_, recoverErr := s.Complete(ctx, session.ID)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if classifyCompletionRecovery(&result, recoverErr) {
			failures = append(failures, fmt.Errorf("recover upload %s completion: %w", session.ID, recoverErr))
		}
	}
	return result, errors.Join(failures...)
}

// RunRecovery performs an immediate bounded scan and then repeats at interval
// until the context is cancelled. A scan error stops the runner so its owner
// can observe and supervise the failure instead of entering a hidden tight
// retry loop.
func (s *Service) RunRecovery(ctx context.Context, interval time.Duration, batchSize int) error {
	if interval <= 0 {
		return errors.New("upload recovery interval must be positive")
	}
	if _, err := s.RecoverDue(ctx, batchSize); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.RecoverDue(ctx, batchSize); err != nil {
				return err
			}
		}
	}
}

func boundedRecoveryBatch(batchSize int) int {
	if batchSize <= 0 {
		return defaultRecoveryBatch
	}
	if batchSize > maxRecoveryBatch {
		return maxRecoveryBatch
	}
	return batchSize
}

func classifyCompletionRecovery(result *RecoveryResult, err error) bool {
	switch {
	case err == nil:
		result.Completed++
	case errors.Is(err, ErrCompletionInProgress):
		result.InProgress++
	case errors.Is(err, ErrCompletionRetryable):
		result.Retryable++
	case errors.Is(err, ErrConflict), errors.Is(err, ErrInvalidSession), errors.Is(err, ErrExpired), errors.Is(err, ErrNotFound):
		result.Terminal++
	default:
		result.Failed++
		return true
	}
	return false
}

func classifyCleanupRecovery(result *RecoveryResult, err error) bool {
	switch {
	case err == nil:
		result.Cleaned++
	case errors.Is(err, ErrCleanupRetryable):
		result.Retryable++
	case errors.Is(err, ErrInvalidSession), errors.Is(err, ErrNotFound):
		result.Terminal++
	default:
		result.Failed++
		return true
	}
	return false
}
