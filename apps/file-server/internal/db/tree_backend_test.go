package db

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyGCSSaveErrorRecognizesTransportPreconditions(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "JSON conflict", err: &googleapi.Error{Code: 409}},
		{name: "JSON precondition", err: &googleapi.Error{Code: 412}},
		{name: "gRPC already exists", err: status.Error(codes.AlreadyExists, "exists")},
		{name: "gRPC precondition", err: status.Error(codes.FailedPrecondition, "generation changed")},
		{name: "wrapped gRPC precondition", err: fmt.Errorf("close writer: %w", status.Error(codes.FailedPrecondition, "generation changed"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyGCSSaveError(test.err); !errors.Is(got, ErrMetadataConflict) {
				t.Fatalf("classifyGCSSaveError() = %v, want ErrMetadataConflict", got)
			}
		})
	}
}

func TestClassifyGCSSaveErrorPreservesNonConflict(t *testing.T) {
	err := status.Error(codes.ResourceExhausted, "quota")
	got := classifyGCSSaveError(err)
	if errors.Is(got, ErrMetadataConflict) {
		t.Fatalf("classifyGCSSaveError() = %v, unexpectedly classified as conflict", got)
	}
	if !errors.Is(got, err) {
		t.Fatalf("classifyGCSSaveError() = %v, want wrapped source error", got)
	}
}
