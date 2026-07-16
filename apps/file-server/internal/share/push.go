package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"google.golang.org/api/idtoken"
)

type PushHandlerConfig struct {
	Audience            string
	ServiceAccountEmail string
}

type Identity struct {
	Email         string
	EmailVerified bool
}

type TokenValidator interface {
	Validate(context.Context, string, string) (Identity, error)
}

type GoogleTokenValidator struct{}

func (GoogleTokenValidator) Validate(ctx context.Context, token, audience string) (Identity, error) {
	payload, err := idtoken.Validate(ctx, token, audience)
	if err != nil {
		return Identity{}, err
	}
	email, _ := payload.Claims["email"].(string)
	verified, _ := payload.Claims["email_verified"].(bool)
	return Identity{Email: email, EmailVerified: verified}, nil
}

type PushHandler struct {
	cfg       PushHandlerConfig
	processor Processor
	validator TokenValidator
	logger    *slog.Logger
}

func NewPushHandler(cfg PushHandlerConfig, processor Processor, validator TokenValidator, logger *slog.Logger) (*PushHandler, error) {
	cfg.Audience = strings.TrimSpace(cfg.Audience)
	cfg.ServiceAccountEmail = strings.TrimSpace(cfg.ServiceAccountEmail)
	if cfg.Audience == "" || cfg.ServiceAccountEmail == "" {
		return nil, errors.New("Pub/Sub push audience and service account email are required")
	}
	if processor == nil {
		return nil, errors.New("share job processor is required")
	}
	if validator == nil {
		validator = GoogleTokenValidator{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PushHandler{cfg: cfg, processor: processor, validator: validator, logger: logger}, nil
}

type pushEnvelope struct {
	Message struct {
		Data       []byte            `json:"data"`
		MessageID  string            `json:"messageId"`
		Attributes map[string]string `json:"attributes"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

func (h *PushHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	identity, err := h.validator.Validate(r.Context(), token, h.cfg.Audience)
	if err != nil {
		h.logger.Warn("reject Pub/Sub push token", "error", err)
		http.Error(w, "invalid bearer token", http.StatusUnauthorized)
		return
	}
	if !identity.EmailVerified || !strings.EqualFold(identity.Email, h.cfg.ServiceAccountEmail) {
		http.Error(w, "push identity is not allowed", http.StatusForbidden)
		return
	}

	var envelope pushEnvelope
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&envelope); err != nil {
		h.logger.Error("discard malformed Pub/Sub envelope", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var job Job
	if err := json.Unmarshal(envelope.Message.Data, &job); err != nil {
		h.logger.Error("discard malformed share job", "message_id", envelope.Message.MessageID, "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := job.Validate(); err != nil {
		h.logger.Error("discard invalid share job", "message_id", envelope.Message.MessageID, "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.processor.ProcessShareJob(r.Context(), job); err != nil {
		if IsPermanent(err) {
			h.logger.Error("discard permanently failed share job", "share_id", job.ShareID, "message_id", envelope.Message.MessageID, "error", err)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.logger.Error("retry share job", "share_id", job.ShareID, "message_id", envelope.Message.MessageID, "error", err)
		http.Error(w, "temporary share job failure", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bearerToken(value string) string {
	scheme, token, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (i Identity) String() string {
	return fmt.Sprintf("%s (verified=%t)", i.Email, i.EmailVerified)
}
