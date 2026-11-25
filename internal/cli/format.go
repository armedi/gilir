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
	if value == nil {
		fmt.Fprintln(w, "ok")
		return
	}
	if text, ok := value.(string); ok {
		writeText(w, text)
		return
	}

	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintln(w, value)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		fmt.Fprintln(w, value)
		return
	}
	for _, line := range formatValue(normalized, 0) {
		fmt.Fprintln(w, line)
	}
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
