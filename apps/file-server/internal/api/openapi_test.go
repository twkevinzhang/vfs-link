package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPIRoute(t *testing.T) {
	handler := (&Server{}).Handler()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(method, "/openapi.json", nil)
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Errorf("HEAD body length = %d, want 0", response.Body.Len())
			}
			if method == http.MethodGet && !json.Valid(response.Body.Bytes()) {
				t.Fatalf("GET body is not valid JSON: %s", response.Body.String())
			}
		})
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got := response.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want GET, HEAD", got)
	}
}

func TestOpenAPIDocumentMetadataAndBasicAuth(t *testing.T) {
	var document struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Components struct {
			SecuritySchemes map[string]struct {
				Type   string `json:"type"`
				Scheme string `json:"scheme"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIDocument, &document); err != nil {
		t.Fatalf("json.Unmarshal(openAPIDocument) error = %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", document.OpenAPI)
	}
	if strings.TrimSpace(document.Info.Title) == "" || strings.TrimSpace(document.Info.Version) == "" {
		t.Errorf("info title/version must be non-empty: %#v", document.Info)
	}

	hasBasicAuth := false
	for _, scheme := range document.Components.SecuritySchemes {
		if scheme.Type == "http" && strings.EqualFold(scheme.Scheme, "basic") {
			hasBasicAuth = true
			break
		}
	}
	if !hasBasicAuth {
		t.Error("components.securitySchemes does not declare HTTP Basic authentication")
	}
}

func TestOpenAPIDocumentCoversPublicRESTOperations(t *testing.T) {
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPIDocument, &document); err != nil {
		t.Fatalf("json.Unmarshal(openAPIDocument) error = %v", err)
	}

	expected := map[string]struct{}{
		"GET /api/status":                       {},
		"GET /api/files":                        {},
		"POST /api/files/move":                  {},
		"POST /api/files/rename":                {},
		"GET /api/operations/{operationId}":     {},
		"POST /api/files/trash":                 {},
		"GET /api/trash":                        {},
		"POST /api/trash/restore":               {},
		"POST /api/trash/delete":                {},
		"POST /api/trash/empty":                 {},
		"GET /api/tree":                         {},
		"GET /api/download":                     {},
		"POST /api/shares/drafts":               {},
		"GET /api/shares/{shareId}":             {},
		"POST /api/shares/{shareId}/start":      {},
		"POST /api/uploads":                     {},
		"GET /api/uploads/{uploadId}":           {},
		"DELETE /api/uploads/{uploadId}":        {},
		"PUT /api/uploads/{uploadId}/content":   {},
		"POST /api/uploads/{uploadId}/complete": {},
		"POST /api/thumbnails":                  {},
		"DELETE /api/thumbnails":                {},
		"GET /api/thumbnails/{thumbnailId}":     {},
		"GET /api/drift":                        {},
		"POST /api/drift/plans":                 {},
		"POST /api/drift/actions":               {},
		"GET /api/drift/actions/{actionId}":     {},
	}

	actual := make(map[string]struct{})
	operationIDs := make(map[string]string)
	for path, pathItem := range document.Paths {
		for method, rawOperation := range pathItem {
			upperMethod := strings.ToUpper(method)
			switch upperMethod {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			default:
				continue
			}

			operation := upperMethod + " " + path
			actual[operation] = struct{}{}
			var details struct {
				OperationID string `json:"operationId"`
			}
			if err := json.Unmarshal(rawOperation, &details); err != nil {
				t.Errorf("decode %s: %v", operation, err)
				continue
			}
			if details.OperationID == "" {
				t.Errorf("%s has no operationId", operation)
			} else if previous, exists := operationIDs[details.OperationID]; exists {
				t.Errorf("operationId %q is used by both %s and %s", details.OperationID, previous, operation)
			} else {
				operationIDs[details.OperationID] = operation
			}
		}
	}

	for operation := range expected {
		if _, exists := actual[operation]; !exists {
			t.Errorf("OpenAPI document is missing %s", operation)
		}
	}
	for operation := range actual {
		if _, exists := expected[operation]; !exists {
			t.Errorf("OpenAPI document has an unexpected public operation %s", operation)
		}
	}
}
