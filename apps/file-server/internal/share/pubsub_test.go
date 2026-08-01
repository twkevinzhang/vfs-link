package share

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

type staticTokenSource struct{}

func (staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "access-token"}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestPubSubDispatcherPublishesVersionedJob(t *testing.T) {
	var requestBody []byte
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		requestBody, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"messageIds":["1"]}`))}, nil
	})}
	dispatcher, err := NewPubSubDispatcherWithTokenSource("project", "topic", staticTokenSource{}, client)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Version: JobVersion, ShareID: "share-1"}
	if err := dispatcher.Dispatch(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	var request struct {
		Messages []struct {
			Data       string            `json:"data"`
			Attributes map[string]string `json:"attributes"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requestBody, &request); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(request.Messages[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != `{"version":1,"shareId":"share-1"}` {
		t.Fatalf("data = %s", decoded)
	}
	if got := request.Messages[0].Attributes["jobId"]; got != job.ID() {
		t.Fatalf("jobId = %q", got)
	}
}

func TestPubSubDispatcherClassifiesClientErrorAsPermanent(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("bad topic"))}, nil
	})}
	dispatcher, err := NewPubSubDispatcherWithTokenSource("project", "topic", staticTokenSource{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), Job{Version: JobVersion, ShareID: "share-1"}); !IsPermanent(err) {
		t.Fatalf("expected permanent error, got %v", err)
	}
}
