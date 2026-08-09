package objectkey

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFromLogicalPathSanitizesPortableSegments(t *testing.T) {
	got, err := FromLogicalPath("docs/A:B/CON.txt/trailing. /cafe\u0301.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "docs/A_B/_CON.txt/trailing__/café.txt"; got != want {
		t.Fatalf("FromLogicalPath() = %q, want %q", got, want)
	}
}

func TestFromLogicalPathSanitizesDotControlsAndDeviceNames(t *testing.T) {
	got, err := FromLogicalPath("./../NUL/COM1.log/LPT9/a\x00b\x7fc")
	if err != nil {
		t.Fatal(err)
	}
	if want := "_/__/_NUL/_COM1.log/_LPT9/a_b_c"; got != want {
		t.Fatalf("FromLogicalPath() = %q, want %q", got, want)
	}
}

func TestFromLogicalPathCollisionIsNotDisambiguated(t *testing.T) {
	first, err := FromLogicalPath("docs/A:B.txt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromLogicalPath("docs/A?B.txt")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != "docs/A_B.txt" {
		t.Fatalf("collision keys = %q and %q", first, second)
	}
}

func TestFromLogicalPathRejectsTooLongKey(t *testing.T) {
	_, err := FromLogicalPath("" + strings.Repeat("a", MaxBytes+1))
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("FromLogicalPath() error = %v, want ErrTooLong", err)
	}
}

func TestFromLogicalPathRejectsEmptySegment(t *testing.T) {
	_, err := FromLogicalPath("docs//report.txt")
	if !errors.Is(err, ErrEmptySegment) {
		t.Fatalf("FromLogicalPath() error = %v, want ErrEmptySegment", err)
	}
}

func TestFromLogicalPathRejectsReservedFirstSegment(t *testing.T) {
	for _, input := range []string{"_vfs-link/file", "_vfs-link-v2/file", "_VFS-LINK-future/file"} {
		if _, err := FromLogicalPath(input); !errors.Is(err, ErrReservedPrefix) {
			t.Errorf("FromLogicalPath(%q) error = %v, want ErrReservedPrefix", input, err)
		}
	}
}

func TestForUploadIsStablePerSessionAndUniqueAcrossSessions(t *testing.T) {
	first, err := ForUpload("docs/report.txt", "upload-a")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := ForUpload("docs/report.txt", "upload-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ForUpload("docs/report.txt", "upload-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != retry {
		t.Fatalf("retry key = %q, want %q", retry, first)
	}
	if first == second {
		t.Fatalf("different upload IDs share key %q", first)
	}
	if !strings.HasPrefix(first, "docs/report.txt.__vfs_upload_") {
		t.Fatalf("upload key = %q", first)
	}
}

func TestForUploadKeepsMaximumLengthAndValidUTF8(t *testing.T) {
	key, err := ForUpload(strings.Repeat("界", 341), "upload")
	if err != nil {
		t.Fatal(err)
	}
	if len(key) > MaxBytes || !utf8.ValidString(key) {
		t.Fatalf("key bytes = %d, valid UTF-8 = %t", len(key), utf8.ValidString(key))
	}
}

func TestForUploadRequiresID(t *testing.T) {
	if _, err := ForUpload("docs/report.txt", "  "); err == nil {
		t.Fatal("ForUpload() error = nil")
	}
}

func TestIsUploadGenerationForPathRejectsOtherPathsAndMalformedSuffixes(t *testing.T) {
	key, err := ForUpload("docs/report.txt", "upload-a")
	if err != nil {
		t.Fatal(err)
	}
	if !IsUploadGenerationForPath("docs/report.txt", key) {
		t.Fatalf("valid upload key %q was rejected", key)
	}
	for _, candidate := range []string{
		key + "extra",
		"other/report.txt" + key[len("docs/report.txt"):],
		"docs/report.txt.__vfs_upload_not-a-digest",
		"docs/report.txt",
	} {
		if IsUploadGenerationForPath("docs/report.txt", candidate) {
			t.Errorf("malformed key %q was accepted", candidate)
		}
	}
}

func TestIsUploadGenerationRequiresExactSession(t *testing.T) {
	key, err := ForUpload("docs/report.txt", "upload-one")
	if err != nil {
		t.Fatal(err)
	}
	if !IsUploadGeneration("docs/report.txt", "upload-one", key) {
		t.Fatal("exact upload generation was not recognized")
	}
	if IsUploadGeneration("docs/report.txt", "upload-two", key) {
		t.Fatal("generation from another upload session was accepted")
	}
}

func TestFromLogicalPathMakesDotSegmentsNonNavigational(t *testing.T) {
	got, err := FromLogicalPath("docs/.././report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "docs/__/_/report.txt" {
		t.Fatalf("FromLogicalPath() = %q", got)
	}
}
