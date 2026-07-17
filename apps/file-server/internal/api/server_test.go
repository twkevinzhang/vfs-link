package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

type rejectListBlobStore struct{ blob.Store }

func (rejectListBlobStore) List(context.Context) ([]blob.ObjectInfo, error) {
	return nil, errors.New("physical object listing must not be used for status")
}

func TestStatsJSONUsesDriverNeutralObjectFields(t *testing.T) {
	payload, err := json.Marshal(Stats{
		ObjectCount: 2,
		ObjectBytes: 3,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	jsonText := string(payload)
	for _, field := range []string{`"objectCount":2`, `"objectBytes":3`} {
		if !strings.Contains(jsonText, field) {
			t.Errorf("Stats JSON = %s, want field %s", jsonText, field)
		}
	}
	for _, legacyField := range []string{"localObjectCount", "localObjectBytes"} {
		if strings.Contains(jsonText, legacyField) {
			t.Errorf("Stats JSON = %s, contains legacy field %q", jsonText, legacyField)
		}
	}
}

func TestStatusUsesMetadataStatsWithoutListingPhysicalBucket(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := db.NewTreeLocal(filepath.Join(root, "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFile(ctx, "/a.txt", "object-a", 3); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}

	var status StatusResponse
	requestJSON(t, New(store, rejectListBlobStore{objects}, nil, "", "").Handler(), http.MethodGet, "/api/status", nil, http.StatusOK, &status)
	if status.Stats.FileCount != 1 || status.Stats.TotalBytes != 3 || status.Stats.ObjectCount != 1 || status.Stats.ObjectBytes != 3 {
		t.Fatalf("status stats = %#v", status.Stats)
	}
}
