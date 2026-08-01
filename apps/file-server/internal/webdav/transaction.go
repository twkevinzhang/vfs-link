package webdav

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
)

var errPreconditionFailed = errors.New("WebDAV write precondition failed")

type uploadSessionKey struct{}

type uploadSession struct {
	mu      sync.Mutex
	uploads []*uploadFile
}

func uploadSessionFromContext(ctx context.Context) *uploadSession {
	session, _ := ctx.Value(uploadSessionKey{}).(*uploadSession)
	return session
}

func (s *uploadSession) add(upload *uploadFile) {
	s.mu.Lock()
	s.uploads = append(s.uploads, upload)
	s.mu.Unlock()
}

func (s *uploadSession) commit() error {
	s.mu.Lock()
	uploads := append([]*uploadFile(nil), s.uploads...)
	s.mu.Unlock()
	for index, upload := range uploads {
		if err := upload.commit(); err != nil {
			for _, pending := range uploads[index+1:] {
				pending.abort()
			}
			return err
		}
	}
	return nil
}

func (s *uploadSession) abort() {
	s.mu.Lock()
	uploads := append([]*uploadFile(nil), s.uploads...)
	s.mu.Unlock()
	for _, upload := range uploads {
		upload.abort()
	}
}

func transactionalWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != "COPY" {
			next.ServeHTTP(w, r)
			return
		}

		session := &uploadSession{}
		buffered := newBufferedResponse()
		next.ServeHTTP(buffered, r.WithContext(context.WithValue(r.Context(), uploadSessionKey{}, session)))
		if buffered.status >= 200 && buffered.status < 300 {
			if err := session.commit(); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, errPreconditionFailed) {
					status = http.StatusPreconditionFailed
				}
				http.Error(w, http.StatusText(status), status)
				return
			}
		} else {
			session.abort()
		}
		buffered.flushTo(w)
	})
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), status: http.StatusOK}
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status != http.StatusOK || status == http.StatusOK {
		return
	}
	w.status = status
}

func (w *bufferedResponse) Write(p []byte) (int, error) { return w.body.Write(p) }

func (w *bufferedResponse) flushTo(destination http.ResponseWriter) {
	for key, values := range w.header {
		for _, value := range values {
			destination.Header().Add(key, value)
		}
	}
	destination.WriteHeader(w.status)
	_, _ = destination.Write(w.body.Bytes())
}

var _ http.ResponseWriter = (*bufferedResponse)(nil)
