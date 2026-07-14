package api

import (
	"encoding/json"
	"strings"
	"testing"
)

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
