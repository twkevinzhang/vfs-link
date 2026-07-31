package drift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const (
	CheckpointPending         = "pending"
	CheckpointCopied          = "copied"
	CheckpointVerified        = "verified"
	CheckpointMetadataUpdated = "metadata_updated"
	CheckpointSourceHandled   = "source_handled"
	CheckpointCompleted       = "completed"
)

func (s *Service) GetPlan(ctx context.Context, id string) (Plan, bool, error) {
	record, ok, err := s.state.FindDriftPlan(ctx, strings.TrimSpace(id))
	if err != nil || !ok {
		return Plan{}, ok, err
	}
	var plan Plan
	err = json.Unmarshal(record.Payload, &plan)
	return plan, err == nil, err
}

func actionID(planID, key string) string {
	sum := sha256.Sum256([]byte(planID + "\x00" + key))
	return "action-" + hex.EncodeToString(sum[:16])
}

func (s *Service) CreateAction(ctx context.Context, planID, idempotencyKey string) (Action, error) {
	planID, idempotencyKey = strings.TrimSpace(planID), strings.TrimSpace(idempotencyKey)
	if planID == "" || idempotencyKey == "" {
		return Action{}, errors.New("planId and idempotencyKey are required")
	}
	_, ok, err := s.GetPlan(ctx, planID)
	if err != nil {
		return Action{}, err
	}
	if !ok {
		return Action{}, errors.New("drift plan not found")
	}
	now := time.Now().UTC()
	action := Action{ID: actionID(planID, idempotencyKey), PlanID: planID, IdempotencyKey: idempotencyKey, Status: "pending", Checkpoint: CheckpointPending, CreatedAt: now, UpdatedAt: now}
	payload, _ := json.Marshal(action)
	record, err := s.state.CreateDriftAction(ctx, db.DriftActionRecord{
		ID: action.ID, PlanID: planID, IdempotencyKey: idempotencyKey,
		Status: action.Status, Checkpoint: action.Checkpoint, Payload: payload,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Action{}, err
	}
	if err := json.Unmarshal(record.Payload, &action); err != nil {
		return Action{}, err
	}
	action.Version = record.Version
	if action.Status == "completed" {
		return action, nil
	}
	if s.autoKick {
		s.KickAction(action.ID)
	}
	return action, nil
}

// KickAction starts or resumes an action without tying its lifetime to the
// HTTP request. A persisted lease plus CAS update prevents two Cloud Run
// instances from executing the same action concurrently. GET may safely kick
// an expired action again after an instance restart.
func (s *Service) KickAction(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, _ = s.ResumeAction(ctx, id)
	}()
}

func (s *Service) ResumeAction(ctx context.Context, id string) (Action, error) {
	action, ok, err := s.GetAction(ctx, id)
	if err != nil || !ok {
		return action, err
	}
	if action.Status == "completed" {
		return action, nil
	}
	now := time.Now().UTC()
	if action.Status == "running" && action.LeaseUntil != nil && action.LeaseUntil.After(now) {
		return action, nil
	}
	lease := now.Add(5 * time.Minute)
	action.Status, action.Error, action.LeaseUntil = "running", "", &lease
	action, err = s.saveAction(ctx, action)
	if errors.Is(err, db.ErrDriftStateConflict) {
		return action, nil
	}
	if err != nil {
		return action, err
	}
	plan, ok, err := s.GetPlan(ctx, action.PlanID)
	if err != nil || !ok {
		return s.fail(ctx, action, errors.New("drift plan not found")), err
	}
	active, trash, err := s.scanRecords(ctx)
	if err != nil {
		return s.fail(ctx, action, err), nil
	}
	refs := make(map[string]int, len(active)+len(trash))
	for _, records := range [][]db.FileRecord{active, trash} {
		for _, record := range records {
			if !record.IsDirectory {
				refs[record.PhysicalHash]++
			}
		}
	}
	return s.execute(ctx, plan, action, refs), nil
}

func (s *Service) GetAction(ctx context.Context, id string) (Action, bool, error) {
	record, ok, err := s.state.FindDriftAction(ctx, strings.TrimSpace(id))
	if err != nil || !ok {
		return Action{}, ok, err
	}
	var action Action
	if err := json.Unmarshal(record.Payload, &action); err != nil {
		return Action{}, false, err
	}
	action.Version = record.Version
	return action, true, nil
}

func (s *Service) saveAction(ctx context.Context, action Action) (Action, error) {
	expected := action.Version
	action.UpdatedAt = time.Now().UTC()
	if action.Status == "running" {
		lease := action.UpdatedAt.Add(5 * time.Minute)
		action.LeaseUntil = &lease
	}
	payload, err := json.Marshal(action)
	if err != nil {
		return action, err
	}
	record, err := s.state.UpdateDriftAction(ctx, db.DriftActionRecord{
		ID: action.ID, PlanID: action.PlanID, IdempotencyKey: action.IdempotencyKey,
		Status: action.Status, Checkpoint: action.Checkpoint, Payload: payload,
		CreatedAt: action.CreatedAt, UpdatedAt: action.UpdatedAt,
	}, expected)
	if err != nil {
		return action, err
	}
	action.Version = record.Version
	return action, nil
}

func (s *Service) fail(ctx context.Context, action Action, err error) Action {
	action.Status, action.Error = "failed", err.Error()
	saved, saveErr := s.saveAction(ctx, action)
	if saveErr != nil {
		action.Error += "; persist failure: " + saveErr.Error()
		return action
	}
	return saved
}

func samePlannedObject(live, planned blob.DriftObject) bool {
	return live.Name == planned.Name && live.Generation == planned.Generation && live.Size == planned.Size && live.Checksum() != "" && live.Checksum() == planned.Checksum()
}

func verifyCopy(source, target blob.DriftObject) error {
	if source.Size != target.Size {
		return fmt.Errorf("copied size mismatch: got %d want %d", target.Size, source.Size)
	}
	if source.Checksum() == "" || target.Checksum() == "" {
		return errors.New("copy checksum is unavailable")
	}
	if source.Checksum() != target.Checksum() {
		return fmt.Errorf("copied checksum mismatch: got %s want %s", target.Checksum(), source.Checksum())
	}
	return nil
}

func (s *Service) execute(ctx context.Context, plan Plan, action Action, refs map[string]int) Action {
	for action.EntryIndex < len(plan.Entries) {
		entry := plan.Entries[action.EntryIndex]
		if action.Checkpoint == CheckpointPending {
			live, err := s.objects.StatDriftObject(ctx, entry.Source.Name)
			if err != nil || !samePlannedObject(live, entry.Source) {
				if err == nil {
					err = errors.New("source object changed after plan creation")
				}
				return s.fail(ctx, action, fmt.Errorf("validate source: %w", err))
			}
			metadata := make(map[string]string, len(live.Metadata)+2)
			for key, value := range live.Metadata {
				metadata[key] = value
			}
			metadata["vfs-link-drift-action"] = action.ID
			metadata["vfs-link-drift-source"] = entry.Source.Name
			target, err := s.objects.CopyDriftObject(ctx, entry.Source.Name, entry.Source.Generation, entry.TargetKey, metadata)
			if errors.Is(err, blob.ErrDriftTargetExists) {
				// A previous copy can have committed while its response/checkpoint
				// was lost. Only our action marker may resolve that ambiguity.
				target, err = s.objects.StatDriftObject(ctx, entry.TargetKey)
				if err == nil && target.Metadata["vfs-link-drift-action"] != action.ID {
					err = blob.ErrDriftTargetExists
				}
			}
			if err != nil {
				return s.fail(ctx, action, err)
			}
			action.Target, action.Checkpoint = &target, CheckpointCopied
			var saveErr error
			if action, saveErr = s.saveAction(ctx, action); saveErr != nil {
				return s.fail(ctx, action, saveErr)
			}
		}
		if action.Checkpoint == CheckpointCopied {
			target, err := s.objects.StatDriftObject(ctx, entry.TargetKey)
			if err != nil {
				return s.fail(ctx, action, err)
			}
			if err := verifyCopy(entry.Source, target); err != nil {
				return s.fail(ctx, action, err)
			}
			action.Target, action.Checkpoint = &target, CheckpointVerified
			if action, err = s.saveAction(ctx, action); err != nil {
				return s.fail(ctx, action, err)
			}
		}
		if action.Checkpoint == CheckpointVerified {
			target, err := s.objects.StatDriftObject(ctx, entry.TargetKey)
			markerRequired := action.Target != nil && action.Target.Metadata["vfs-link-drift-action"] != ""
			if err != nil || action.Target == nil || target.Generation != action.Target.Generation || (markerRequired && target.Metadata["vfs-link-drift-action"] != action.ID) {
				if err == nil {
					err = errors.New("target object changed or action marker is missing")
				}
				return s.fail(ctx, action, fmt.Errorf("revalidate target: %w", err))
			}
			if err := verifyCopy(entry.Source, target); err != nil {
				return s.fail(ctx, action, err)
			}
			expected := entry.Source.Name
			_, matched, err := s.metadata.ReplaceFileConditional(ctx, entry.LogicPath, entry.TargetKey, entry.Source.Size, &expected, false)
			if err != nil {
				return s.fail(ctx, action, fmt.Errorf("update metadata: %w", err))
			}
			if !matched {
				current, found, findErr := s.metadata.Find(ctx, entry.LogicPath)
				if findErr != nil || !found || current.PhysicalHash != entry.TargetKey {
					return s.fail(ctx, action, errors.New("metadata changed after plan creation"))
				}
			} else if refs[entry.Source.Name] > 0 {
				refs[entry.Source.Name]--
			}
			action.Checkpoint = CheckpointMetadataUpdated
			if action, err = s.saveAction(ctx, action); err != nil {
				return s.fail(ctx, action, err)
			}
		}
		if action.Checkpoint == CheckpointMetadataUpdated {
			if refs[entry.Source.Name] == 0 {
				if err := s.objects.DeleteDriftObject(ctx, entry.Source.Name, entry.Source.Generation); err != nil {
					return s.fail(ctx, action, err)
				}
			}
			action.Checkpoint = CheckpointSourceHandled
			var err error
			if action, err = s.saveAction(ctx, action); err != nil {
				return s.fail(ctx, action, err)
			}
		}
		if action.Checkpoint == CheckpointSourceHandled {
			action.EntryIndex++
			action.Target = nil
			action.Checkpoint = CheckpointPending
			if action.EntryIndex < len(plan.Entries) {
				var err error
				if action, err = s.saveAction(ctx, action); err != nil {
					return s.fail(ctx, action, err)
				}
			}
		}
	}
	now := time.Now().UTC()
	action.Status, action.Checkpoint, action.CompletedAt, action.LeaseUntil = "completed", CheckpointCompleted, &now, nil
	saved, err := s.saveAction(ctx, action)
	if err != nil {
		return s.fail(ctx, action, err)
	}
	return saved
}

func (s *Service) scanRecords(ctx context.Context) ([]db.FileRecord, []db.FileRecord, error) {
	if scanner, ok := s.metadata.(db.DriftRecordScanner); ok {
		return scanner.ScanDriftRecords(ctx)
	}
	active, err := s.metadata.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	trash, err := s.metadata.ListTrashRecords(ctx, nil)
	return active, trash, err
}
