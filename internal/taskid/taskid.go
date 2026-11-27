// Package taskid defines the canonical identifier for Gilir tasks.
//
// A task ID is the literal prefix "GILIR-" followed by the positive decimal
// sequence number allocated when the task was created, for example
// "GILIR-42". The prefix is self-describing: an agent that meets the ID in a
// prompt, a branch name, or a commit message can tell that it belongs to Gilir
// task management.
package taskid

import (
	"fmt"
	"strconv"
	"strings"
)

// Prefix starts every task ID.
const Prefix = "GILIR-"

// maxLength bounds the digits after the prefix so the value always fits in an
// int64 sequence.
const maxLength = 19

// Format renders a positive sequence number as a canonical task ID.
func Format(seq int64) string {
	return Prefix + strconv.FormatInt(seq, 10)
}

// Parse validates a task ID and returns its sequence number. The prefix is
// matched case-insensitively so "gilir-42" is accepted, but the digits must be
// positive decimal without sign, spaces, or leading zeros.
func Parse(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= len(Prefix) || !strings.EqualFold(trimmed[:len(Prefix)], Prefix) {
		return 0, fmt.Errorf("invalid task id %q: expected %s<positive integer>", value, Prefix)
	}
	digits := trimmed[len(Prefix):]
	if len(digits) > maxLength || digits[0] == '0' {
		return 0, fmt.Errorf("invalid task id %q: expected %s<positive integer>", value, Prefix)
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid task id %q: expected %s<positive integer>", value, Prefix)
		}
	}
	seq, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || seq <= 0 {
		return 0, fmt.Errorf("invalid task id %q: expected %s<positive integer>", value, Prefix)
	}
	return seq, nil
}

// Normalize returns the canonical spelling of a task ID.
func Normalize(value string) (string, error) {
	seq, err := Parse(value)
	if err != nil {
		return "", err
	}
	return Format(seq), nil
}
