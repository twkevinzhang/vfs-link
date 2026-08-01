package logicpath

import "testing"

func TestCanonicalRelativeContract(t *testing.T) {
	tests := map[string]string{
		"":                 "",
		".":                "",
		"AHR/comic/file":   "AHR/comic/file",
		"AHR//comic/../x":  "AHR/x",
		"AHR/cafe\u0301":   "AHR/caf\u00e9",
		"/legacy/absolute": "legacy/absolute",
	}
	for input, want := range tests {
		if got := Clean(input); got != want {
			t.Fatalf("Clean(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseRejectsLegacyAbsolutePath(t *testing.T) {
	if _, err := Parse("/AHR/file"); err != ErrAbsolute {
		t.Fatalf("Parse absolute error = %v, want %v", err, ErrAbsolute)
	}
	if got, err := Parse("AHR/file"); err != nil || got != "AHR/file" {
		t.Fatalf("Parse relative = %q, %v", got, err)
	}
}

func TestProtocolBoundary(t *testing.T) {
	if got := FromProtocol("/AHR/file"); got != "AHR/file" {
		t.Fatalf("FromProtocol = %q", got)
	}
	if got := ToProtocol("AHR/file"); got != "/AHR/file" {
		t.Fatalf("ToProtocol = %q", got)
	}
	if got := ToProtocol(""); got != "/" {
		t.Fatalf("root ToProtocol = %q", got)
	}
}
