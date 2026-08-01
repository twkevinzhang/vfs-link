package httpauth

import (
	"crypto/subtle"
	"net/http"
)

// Basic protects the browser and public API with one application credential.
// Internal machine-to-machine endpoints should be mounted outside this wrapper
// and enforce their own identity tokens.
func Basic(enabled bool, user, pass string, next http.Handler) http.Handler {
	if !enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualUser, actualPass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(actualUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(actualPass), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="vfs-link", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
