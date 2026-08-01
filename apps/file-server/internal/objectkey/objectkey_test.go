package objectkey

import (
	"errors"
	"strings"
	"testing"
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

func TestFromLogicalPathMakesDotSegmentsNonNavigational(t *testing.T) {
	got, err := FromLogicalPath("docs/.././report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "docs/__/_/report.txt" {
		t.Fatalf("FromLogicalPath() = %q", got)
	}
}
