package taskid

import "testing"

func TestFormatAndParse(t *testing.T) {
	if got := Format(42); got != "GILIR-42" {
		t.Fatalf("Format(42) = %q, want GILIR-42", got)
	}

	for _, test := range []struct {
		in   string
		want int64
	}{
		{"GILIR-1", 1},
		{"GILIR-42", 42},
		{"gilir-42", 42},
		{"  GILIR-7  ", 7},
		{"GILIR-9223372036854775807", 9223372036854775807},
	} {
		got, err := Parse(test.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.in, err)
		}
		if got != test.want {
			t.Fatalf("Parse(%q) = %d, want %d", test.in, got, test.want)
		}
	}
}

func TestParseRejectsMalformedIDs(t *testing.T) {
	for _, value := range []string{
		"",
		"42",
		"0",
		"GILIR-",
		"GILIR-0",
		"GILIR-007",
		"GILIR--1",
		"GILIR-+1",
		"GILIR-1.0",
		"GILIR-1x",
		"TASK-1",
		"GILIR-9223372036854775808",
	} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("Parse(%q) accepted a malformed ID", value)
		}
	}
}

func TestNormalizeCanonicalizesCase(t *testing.T) {
	got, err := Normalize("gilir-7")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got != "GILIR-7" {
		t.Fatalf("Normalize(\"gilir-7\") = %q, want GILIR-7", got)
	}
}
