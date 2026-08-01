// Package objectkey maps logical VFS paths to portable physical object names.
package objectkey

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxBytes is Cloud Storage's maximum UTF-8 object-name length.
const MaxBytes = 1024

var ErrTooLong = errors.New("sanitized object key is too long")

var (
	ErrEmptySegment   = errors.New("logical path contains an empty segment")
	ErrReservedPrefix = errors.New("sanitized object key uses a reserved prefix")
)

// FromLogicalPath returns a deterministic, portable object key. It deliberately
// does not add a suffix when two logical paths sanitize to the same key: the
// storage generation precondition is responsible for reporting that collision.
func FromLogicalPath(logicalPath string) (string, error) {
	if !utf8.ValidString(logicalPath) {
		return "", errors.New("logical path is not valid UTF-8")
	}
	logicalPath = norm.NFC.String(logicalPath)
	if strings.HasPrefix(logicalPath, "/") {
		return "", errors.New("logical path must not start with a slash")
	}
	if logicalPath == "" {
		return "", errors.New("logical path does not name an object")
	}

	segments := strings.Split(logicalPath, "/")
	for index, segment := range segments {
		if segment == "" {
			return "", ErrEmptySegment
		}
		segments[index] = sanitizeSegment(segment)
	}
	if strings.HasPrefix(strings.ToLower(segments[0]), "_vfs-link") {
		return "", ErrReservedPrefix
	}
	key := strings.Join(segments, "/")
	if len([]byte(key)) > MaxBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds %d", ErrTooLong, len([]byte(key)), MaxBytes)
	}
	return key, nil
}

func sanitizeSegment(segment string) string {
	var result strings.Builder
	result.Grow(len(segment))
	for _, r := range segment {
		if isIllegal(r) {
			result.WriteByte('_')
		} else {
			result.WriteRune(r)
		}
	}
	value := result.String()
	if value == "." {
		value = "_"
	} else if value == ".." {
		value = "__"
	}

	// Windows strips trailing spaces and dots, so replace each one to keep the
	// segment addressable and deterministic on both local and object storage.
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0 && (runes[index] == ' ' || runes[index] == '.'); index-- {
		runes[index] = '_'
	}
	value = string(runes)

	if isWindowsDeviceName(value) {
		value = "_" + value
	}
	return value
}

func isIllegal(r rune) bool {
	if r <= 0x1f || r == 0x7f {
		return true
	}
	switch r {
	case '<', '>', ':', '"', '\\', '|', '?', '*':
		return true
	default:
		return false
	}
}

func isWindowsDeviceName(segment string) bool {
	base := segment
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}
