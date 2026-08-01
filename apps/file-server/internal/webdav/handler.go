package webdav

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	xwebdav "golang.org/x/net/webdav"
)

type Config struct {
	Prefix                string
	User                  string
	Pass                  string
	LockTimeout           time.Duration
	TrustForwardedHeaders bool
}

type writeConditionKey struct{}

type writeCondition struct {
	path                 string
	expectedPhysicalHash *string
	requireAbsent        bool
}

func writeConditionFromContext(ctx context.Context) *writeCondition {
	condition, _ := ctx.Value(writeConditionKey{}).(*writeCondition)
	return condition
}

func New(cfg Config, store db.Store, objects blob.Store, logger *slog.Logger) http.Handler {
	prefix := normalizePrefix(cfg.Prefix)
	fs := NewFileSystem(store, objects)
	ls := NewLockSystem(store, cfg.LockTimeout)
	dav := &xwebdav.Handler{
		Prefix:     prefix,
		FileSystem: fs,
		LockSystem: ls,
		Logger: func(r *http.Request, err error) {
			logger.Error("WebDAV request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		},
	}
	return secureRequests(prefix, cfg.TrustForwardedHeaders, basicAuth(cfg.User, cfg.Pass, transactionalWrites(conditionalRequests(prefix, fs, dav))))
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualUser, actualPass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(actualUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(actualPass), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="vfs-link WebDAV", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureRequests(prefix string, trustForwardedHeaders bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestIsHTTPS(r, trustForwardedHeaders) {
			http.Error(w, "WebDAV requires HTTPS", http.StatusUpgradeRequired)
			return
		}
		if r.Method == "PROPFIND" && strings.EqualFold(strings.TrimSpace(r.Header.Get("Depth")), "infinity") {
			http.Error(w, "Depth: infinity is not supported", http.StatusForbidden)
			return
		}
		if mutatesRoot(r.Method) && cleanRequestPath(r.URL.Path) == strings.TrimSuffix(prefix, "/") {
			http.Error(w, "the WebDAV root cannot be modified", http.StatusForbidden)
			return
		}
		if destination := strings.TrimSpace(r.Header.Get("Destination")); destination != "" {
			u, err := url.Parse(destination)
			if err != nil || (u.Host != "" && !strings.EqualFold(u.Host, r.Host)) || !pathWithinPrefix(u.Path, prefix) {
				http.Error(w, "invalid WebDAV destination", http.StatusBadGateway)
				return
			}
			if (r.Method == "MOVE" || r.Method == "COPY") && isSelfOrDescendant(r.URL.Path, u.Path) {
				http.Error(w, "destination cannot be the source or its descendant", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestIsHTTPS(r *http.Request, trustForwardedHeaders bool) bool {
	if r.TLS != nil {
		return true
	}
	return trustForwardedHeaders && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func mutatesRoot(method string) bool {
	switch method {
	case http.MethodPut, http.MethodDelete, "MKCOL", "MOVE", "COPY":
		return true
	default:
		return false
	}
}

func conditionalRequests(prefix string, fs *FileSystem, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isConditionalWrite(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
		ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
		if ifMatch == "" && ifNoneMatch == "" {
			next.ServeHTTP(w, r)
			return
		}

		name := strings.TrimPrefix(cleanRequestPath(r.URL.Path), strings.TrimSuffix(prefix, "/"))
		if name == "" {
			name = "/"
		}
		info, err := fs.Stat(r.Context(), name)
		exists := err == nil
		etag := ""
		if exists {
			if etager, ok := info.(xwebdav.ETager); ok {
				etag, err = etager.ETag(r.Context())
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if ifMatch != "" && !etagConditionMatches(ifMatch, etag, exists) {
			http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
			return
		}
		if ifMatch == "" && ifNoneMatch != "" && etagConditionMatches(ifNoneMatch, etag, exists) {
			http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
			return
		}
		if r.Method == http.MethodPut {
			condition := &writeCondition{path: name}
			switch {
			case ifMatch != "" && exists:
				if current, ok := info.(fileInfo); ok && !current.directory {
					expected := current.physicalHash
					condition.expectedPhysicalHash = &expected
				}
			case ifNoneMatch == "*" && !exists:
				condition.requireAbsent = true
			}
			if condition.expectedPhysicalHash != nil || condition.requireAbsent {
				r = r.WithContext(context.WithValue(r.Context(), writeConditionKey{}, condition))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isSelfOrDescendant(source, destination string) bool {
	source = cleanRequestPath(source)
	destination = cleanRequestPath(destination)
	return destination == source || strings.HasPrefix(destination, strings.TrimSuffix(source, "/")+"/")
}

func isConditionalWrite(method string) bool {
	switch method {
	case http.MethodPut, http.MethodDelete, "MKCOL", "MOVE", "COPY", "PROPPATCH":
		return true
	default:
		return false
	}
}

func etagConditionMatches(header, etag string, exists bool) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return exists
		}
		if exists && candidate == etag {
			return true
		}
	}
	return false
}
