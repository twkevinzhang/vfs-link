package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const pubSubScope = "https://www.googleapis.com/auth/pubsub"

type PubSubDispatcher struct {
	topicURL    string
	tokenSource oauth2.TokenSource
	client      *http.Client
}

func NewPubSubDispatcher(ctx context.Context, projectID, topicID string) (*PubSubDispatcher, error) {
	tokenSource, err := google.DefaultTokenSource(ctx, pubSubScope)
	if err != nil {
		return nil, fmt.Errorf("create Pub/Sub credentials: %w", err)
	}
	return NewPubSubDispatcherWithTokenSource(projectID, topicID, tokenSource, http.DefaultClient)
}

func NewPubSubDispatcherWithTokenSource(projectID, topicID string, tokenSource oauth2.TokenSource, client *http.Client) (*PubSubDispatcher, error) {
	projectID = strings.TrimSpace(projectID)
	topicID = strings.TrimSpace(topicID)
	if projectID == "" || topicID == "" {
		return nil, errors.New("Pub/Sub project ID and topic ID are required")
	}
	if tokenSource == nil {
		return nil, errors.New("Pub/Sub token source is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	topicURL := fmt.Sprintf("https://pubsub.googleapis.com/v1/projects/%s/topics/%s:publish", url.PathEscape(projectID), url.PathEscape(topicID))
	return &PubSubDispatcher{topicURL: topicURL, tokenSource: tokenSource, client: client}, nil
}

func (d *PubSubDispatcher) Dispatch(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return Permanent(err)
	}
	data, err := json.Marshal(job)
	if err != nil {
		return Permanent(fmt.Errorf("encode share job: %w", err))
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{
			"data":       data,
			"attributes": map[string]string{"jobId": job.ID()},
		}},
	})
	if err != nil {
		return Permanent(fmt.Errorf("encode Pub/Sub request: %w", err))
	}
	token, err := d.tokenSource.Token()
	if err != nil {
		return fmt.Errorf("get Pub/Sub access token: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.topicURL, bytes.NewReader(body))
	if err != nil {
		return Permanent(fmt.Errorf("create Pub/Sub request: %w", err))
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("publish share job: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		err := fmt.Errorf("publish share job status %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
		if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
			return Permanent(err)
		}
		return err
	}
	return nil
}
