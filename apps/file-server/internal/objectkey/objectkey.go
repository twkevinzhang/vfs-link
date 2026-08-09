// Package objectkey maps logical VFS paths to portable physical object names.
package objectkey

import (
	"crypto/sha256"
	"encoding/base64"
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

const uploadGenerationMarker = ".__vfs_upload_"

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

// ForUpload returns an immutable object identity for one upload session. The
// logical-path portion remains recognizable to operators, while the digest
// makes retries of the same upload converge on one object and prevents two
// uploads to the same path from overwriting each other before metadata CAS.
//
// The key intentionally does not use the reserved _vfs-link prefix because
// reserved objects are excluded from drift scans.
func ForUpload(logicalPath, uploadID string) (string, error) {
	base, err := FromLogicalPath(logicalPath)
	if err != nil {
		return "", err
	}
	uploadID = strings.TrimSpace(uploadID)
	if uploadID == "" {
		return "", errors.New("upload ID is required")
	}
	digest := sha256.Sum256([]byte(uploadID))
	suffix := uploadGenerationMarker + base64.RawURLEncoding.EncodeToString(digest[:])
	base = truncateUTF8(base, MaxBytes-len(suffix))
	if base == "" {
		return "", ErrTooLong
	}
	return base + suffix, nil
}

// IsUploadGenerationForPath reports whether key has the exact shape emitted
// by ForUpload for logicalPath. It does not prove which session produced the
// digest; callers use it to distinguish current immutable upload generations
// from legacy drifted object names.
func IsUploadGenerationForPath(logicalPath, key string) bool {
	base, err := FromLogicalPath(logicalPath)
	if err != nil {
		return false
	}
	digestLength := base64.RawURLEncoding.EncodedLen(sha256.Size)
	suffixLength := len(uploadGenerationMarker) + digestLength
	base = truncateUTF8(base, MaxBytes-suffixLength)
	prefix := base + uploadGenerationMarker
	encoded, ok := strings.CutPrefix(key, prefix)
	if !ok || len(encoded) != digestLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size
}

// IsUploadGeneration reports whether key is the immutable object identity for
// this exact upload session. It is stricter than IsUploadGenerationForPath and
// keeps pre-migration fixed-key sessions out of generation-based idempotency.
func IsUploadGeneration(logicalPath, uploadID, key string) bool {
	expected, err := ForUpload(logicalPath, uploadID)
	return err == nil && key == expected
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
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
