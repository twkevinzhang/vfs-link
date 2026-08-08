package db

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOperationTypeValidationAndTreeV4Support(t *testing.T) {
	tests := []struct {
		operationType OperationType
		valid         bool
		treeV4        bool
	}{
		{operationType: OperationTypeMove, valid: true, treeV4: true},
		{operationType: OperationTypeRename, valid: true, treeV4: true},
		{operationType: OperationTypeTrash, valid: true, treeV4: true},
		{operationType: OperationTypeRestore, valid: true, treeV4: true},
		{operationType: OperationTypeDeleteTrash, valid: true},
		{operationType: OperationType("copy")},
	}

	for _, test := range tests {
		t.Run(string(test.operationType), func(t *testing.T) {
			if got := test.operationType.Valid(); got != test.valid {
				t.Fatalf("Valid() = %t, want %t", got, test.valid)
			}
			if got := test.operationType.SupportedByTreeV4(); got != test.treeV4 {
				t.Fatalf("SupportedByTreeV4() = %t, want %t", got, test.treeV4)
			}
		})
	}
}

func TestOperationStatusLifecycle(t *testing.T) {
	tests := []struct {
		name     string
		status   OperationStatus
		valid    bool
		terminal bool
		runnable bool
	}{
		{name: "pending", status: OperationStatusPending, valid: true, runnable: true},
		{name: "running", status: OperationStatusRunning, valid: true, runnable: true},
		{name: "completed", status: OperationStatusCompleted, valid: true, terminal: true},
		{name: "failed", status: OperationStatusFailed, valid: true, terminal: true},
		{name: "unknown remains decodable", status: OperationStatus("paused")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.status.Valid(); got != test.valid {
				t.Fatalf("Valid() = %t, want %t", got, test.valid)
			}
			if got := test.status.Terminal(); got != test.terminal {
				t.Fatalf("Terminal() = %t, want %t", got, test.terminal)
			}
			if got := test.status.Runnable(); got != test.runnable {
				t.Fatalf("Runnable() = %t, want %t", got, test.runnable)
			}
		})
	}
}

func TestOperationStatusCanTransitionTo(t *testing.T) {
	tests := []struct {
		name string
		from OperationStatus
		to   OperationStatus
		want bool
	}{
		{name: "pending starts", from: OperationStatusPending, to: OperationStatusRunning, want: true},
		{name: "expired running lease can be reacquired", from: OperationStatusRunning, to: OperationStatusRunning, want: true},
		{name: "cancellation returns to pending", from: OperationStatusRunning, to: OperationStatusPending, want: true},
		{name: "success completes", from: OperationStatusRunning, to: OperationStatusCompleted, want: true},
		{name: "permanent failure terminates", from: OperationStatusRunning, to: OperationStatusFailed, want: true},
		{name: "pending cannot skip running", from: OperationStatusPending, to: OperationStatusCompleted},
		{name: "completed is terminal", from: OperationStatusCompleted, to: OperationStatusRunning},
		{name: "failed is terminal", from: OperationStatusFailed, to: OperationStatusRunning},
		{name: "unknown legacy status preserves runner compatibility", from: OperationStatus("paused"), to: OperationStatusRunning, want: true},
		{name: "unknown legacy status cannot complete directly", from: OperationStatus("paused"), to: OperationStatusCompleted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.from.CanTransitionTo(test.to); got != test.want {
				t.Fatalf("CanTransitionTo(%q) = %t, want %t", test.to, got, test.want)
			}
		})
	}
}

func TestOperationRecordNeedsKick(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Second)
	active := now.Add(time.Second)
	tests := []struct {
		name       string
		status     OperationStatus
		leaseUntil *time.Time
		active     bool
		needsKick  bool
	}{
		{name: "pending without lease", status: OperationStatusPending, needsKick: true},
		{name: "pending retains legacy behavior with future lease", status: OperationStatusPending, leaseUntil: &active, needsKick: true},
		{name: "running without lease", status: OperationStatusRunning, needsKick: true},
		{name: "running expired lease", status: OperationStatusRunning, leaseUntil: &expired, needsKick: true},
		{name: "running active lease", status: OperationStatusRunning, leaseUntil: &active, active: true},
		{name: "completed", status: OperationStatusCompleted},
		{name: "failed", status: OperationStatusFailed},
		{name: "unknown", status: OperationStatus("paused")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := OperationRecord{Type: OperationTypeMove, Status: test.status, LeaseUntil: test.leaseUntil}
			if got := operation.HasActiveLease(now); got != test.active {
				t.Fatalf("HasActiveLease() = %t, want %t", got, test.active)
			}
			if got := operation.NeedsKick(now); got != test.needsKick {
				t.Fatalf("NeedsKick() = %t, want %t", got, test.needsKick)
			}
		})
	}
}

func TestOperationRecordJSONCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantType   OperationType
		wantStatus OperationStatus
	}{
		{name: "known values", payload: `{"id":"known","type":"move","status":"pending","paths":[]}`, wantType: OperationTypeMove, wantStatus: OperationStatusPending},
		{name: "unknown legacy values", payload: `{"id":"legacy","type":"future-copy","status":"paused","paths":[]}`, wantType: OperationType("future-copy"), wantStatus: OperationStatus("paused")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var operation OperationRecord
			if err := json.Unmarshal([]byte(test.payload), &operation); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if operation.Type != test.wantType || operation.Status != test.wantStatus {
				t.Fatalf("decoded type/status = %q/%q, want %q/%q", operation.Type, operation.Status, test.wantType, test.wantStatus)
			}

			encoded, err := json.Marshal(operation)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var roundTrip OperationRecord
			if err = json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatalf("round-trip Unmarshal() error = %v", err)
			}
			if roundTrip.Type != test.wantType || roundTrip.Status != test.wantStatus {
				t.Fatalf("round-trip type/status = %q/%q, want %q/%q", roundTrip.Type, roundTrip.Status, test.wantType, test.wantStatus)
			}
		})
	}
}
