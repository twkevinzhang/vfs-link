package share

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type tokenValidatorFunc func(context.Context, string, string) (Identity, error)

func (f tokenValidatorFunc) Validate(ctx context.Context, token, audience string) (Identity, error) {
	return f(ctx, token, audience)
}

type processorFunc func(context.Context, Job) error

func (f processorFunc) ProcessShareJob(ctx context.Context, job Job) error { return f(ctx, job) }

func newTestPushHandler(t *testing.T, processor Processor) *PushHandler {
	t.Helper()
	handler, err := NewPushHandler(
		PushHandlerConfig{Audience: "https://service/internal/pubsub/shares", ServiceAccountEmail: "push@example.iam.gserviceaccount.com"},
		processor,
		tokenValidatorFunc(func(_ context.Context, token, audience string) (Identity, error) {
			if token != "valid" || audience != "https://service/internal/pubsub/shares" {
				return Identity{}, errors.New("invalid")
			}
			return Identity{Email: "push@example.iam.gserviceaccount.com", EmailVerified: true}, nil
		}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func pushRequest(data string) *http.Request {
	body := `{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(data)) + `","messageId":"message-1"},"subscription":"sub"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/pubsub/shares", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer valid")
	return request
}

func TestPushHandlerProcessesAuthenticatedJob(t *testing.T) {
	var received Job
	handler := newTestPushHandler(t, processorFunc(func(_ context.Context, job Job) error {
		received = job
		return nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, pushRequest(`{"version":1,"shareId":"share-1"}`))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if received.ShareID != "share-1" {
		t.Fatalf("share ID = %q", received.ShareID)
	}
}

func TestPushHandlerRetriesTemporaryFailure(t *testing.T) {
	handler := newTestPushHandler(t, processorFunc(func(context.Context, Job) error {
		return errors.New("temporary")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, pushRequest(`{"version":1,"shareId":"share-1"}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestPushHandlerAcknowledgesPermanentFailure(t *testing.T) {
	handler := newTestPushHandler(t, processorFunc(func(context.Context, Job) error {
		return Permanent(errors.New("permanent"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, pushRequest(`{"version":1,"shareId":"share-1"}`))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestPushHandlerRejectsWrongIdentity(t *testing.T) {
	handler, err := NewPushHandler(
		PushHandlerConfig{Audience: "audience", ServiceAccountEmail: "expected@example.com"},
		processorFunc(func(context.Context, Job) error { return nil }),
		tokenValidatorFunc(func(context.Context, string, string) (Identity, error) {
			return Identity{Email: "other@example.com", EmailVerified: true}, nil
		}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := pushRequest(`{"version":1,"shareId":"share-1"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d", response.Code)
	}
}
