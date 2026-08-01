// Package logicpath defines the canonical logical-path contract used by the
// domain, API, and metadata stores. Logical paths deliberately mirror GCS
// object keys: they are NFC-normalized, slash-separated, and never begin with
// a slash. The root directory is the empty string.
package logicpath

import (
	"errors"
	pathpkg "path"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var ErrAbsolute = errors.New("logical path must not start with a slash")

// Clean canonicalizes a trusted domain or migration value. Boundary code must
// use Parse for new API input so legacy absolute logical paths are rejected.
func Clean(value string) string {
	value = norm.NFC.String(strings.TrimSpace(value))
	value = strings.TrimLeft(value, "/")
	if value == "" || value == "." {
		return ""
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." || cleaned == "/" {
		return ""
	}
	return strings.TrimLeft(cleaned, "/")
}

// Parse validates new domain input and returns its canonical relative form.
func Parse(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "/") {
		return "", ErrAbsolute
	}
	return Clean(trimmed), nil
}

// FromProtocol converts an absolute WebDAV/FTP/VFS path at the protocol
// boundary into the canonical domain representation.
func FromProtocol(value string) string { return Clean(value) }

// ToProtocol converts a canonical domain path to an absolute protocol path.
func ToProtocol(value string) string {
	value = Clean(value)
	if value == "" {
		return "/"
	}
	return "/" + value
}

func Parent(value string) string { return Clean(pathpkg.Dir(Clean(value))) }

func Join(base, name string) string { return Clean(pathpkg.Join(Clean(base), name)) }

func IsDescendant(ancestor, descendant string) bool {
	ancestor = Clean(ancestor)
	descendant = Clean(descendant)
	if ancestor == "" {
		return descendant != ""
	}
	return strings.HasPrefix(descendant, ancestor+"/")
}

func WithTrailingSlash(value string) string {
	value = Clean(value)
	if value == "" {
		return ""
	}
	return value + "/"
}
