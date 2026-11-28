package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

type errorOutput struct {
	Error string `json:"error"`
}

type helpOutput struct {
	Help string `json:"help"`
}

type skillOutput struct {
	Skill string `json:"skill"`
}

type versionOutput struct {
	Version string `json:"version"`
}

func writeFailure(w io.Writer, jsonMode bool, message string) {
	if jsonMode {
		if err := writeJSON(w, errorOutput{Error: message}); err == nil {
			return
		}
	}
	fmt.Fprintf(w, "gilir: %s\n", message)
}

func writeJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func writeText(w io.Writer, text string) {
	if text == "" {
		fmt.Fprintln(w)
		return
	}
	io.WriteString(w, text)
	if !strings.HasSuffix(text, "\n") {
		io.WriteString(w, "\n")
	}
}

func writeHuman(w io.Writer, value any) {
	if humanText(w, value) {
		return
	}
	for _, line := range formatValue(normalize(value), 0) {
		fmt.Fprintln(w, line)
	}
}

// humanText handles the values that need no structural formatting.
func humanText(w io.Writer, value any) bool {
	if value == nil {
		fmt.Fprintln(w, "ok")
		return true
	}
	if text, ok := value.(string); ok {
		writeText(w, text)
		return true
	}
	return false
}

// normalize decodes any JSON-carrying value into generic Go values with exact
// numbers, so the text formatter can walk it.
func normalize(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return value
	}
	return normalized
}

func formatValue(value any, depth int) []string {
	padding := strings.Repeat("  ", depth)
	switch typed := value.(type) {
	case nil:
		return []string{padding + "null"}
	case string:
		lines := strings.Split(typed, "\n")
		for i := range lines {
			lines[i] = padding + lines[i]
		}
		return lines
	case bool:
		return []string{padding + strconv.FormatBool(typed)}
	case json.Number:
		return []string{padding + typed.String()}
	case float64:
		return []string{padding + strconv.FormatFloat(typed, 'g', -1, 64)}
	case map[string]any:
		if len(typed) == 0 {
			return []string{padding + "{}"}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var lines []string
		for _, key := range keys {
			child := formatValue(typed[key], depth+1)
			if isInlineValue(typed[key]) && len(child) == 1 {
				lines = append(lines, padding+key+": "+strings.TrimPrefix(child[0], padding+"  "))
				continue
			}
			lines = append(lines, padding+key+":")
			lines = append(lines, child...)
		}
		return lines
	case []any:
		if len(typed) == 0 {
			return []string{padding + "[]"}
		}
		childPadding := padding + "  "
		var lines []string
		for _, item := range typed {
			child := formatValue(item, depth+1)
			lines = append(lines, padding+"- "+strings.TrimPrefix(child[0], childPadding))
			lines = append(lines, child[1:]...)
		}
		return lines
	default:
		return []string{padding + fmt.Sprint(typed)}
	}
}

func isInlineValue(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

// detailFieldIndex lists the sections in reading order. The generic formatter
// sorts keys alphabetically, which buries the task below its activity.
var detailFieldIndex = map[string]int{
	"task":     0,
	"blockers": 1,
	"sessions": 2,
	"activity": 3,
}

// writeTaskDetail prints a TaskDetail in reading order: the task, then its
// blockers, sessions (including each session's worktree) and activity. The
// JSON form is unchanged and remains the machine-readable contract.
func writeTaskDetail(w io.Writer, value any) error {
	normalized := normalize(value)
	detail, ok := normalized.(map[string]any)
	if !ok {
		return writeHumanErr(w, value)
	}

	sections := make([]string, 0, len(detail))
	for key := range detail {
		if _, known := detailFieldIndex[key]; known {
			sections = append(sections, key)
		}
	}
	sort.Slice(sections, func(i, j int) bool { return detailFieldIndex[sections[i]] < detailFieldIndex[sections[j]] })
	for _, key := range sections {
		for _, line := range formatValue(map[string]any{key: detail[key]}, 0) {
			fmt.Fprintln(w, line)
		}
	}

	// Any unexpected field still prints, after the known sections.
	rest := map[string]any{}
	for key, item := range detail {
		if _, known := detailFieldIndex[key]; !known {
			rest[key] = item
		}
	}
	if len(rest) > 0 {
		for _, line := range formatValue(rest, 0) {
			fmt.Fprintln(w, line)
		}
	}
	return nil
}

// writeHumanErr falls back to the generic renderer for an unexpected shape.
func writeHumanErr(w io.Writer, value any) error {
	writeHuman(w, value)
	return nil
}
