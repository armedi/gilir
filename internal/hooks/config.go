// Package hooks delivers audit events to locally configured command and
// HTTP(S) webhook destinations.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Config is one hook entry from the service's hooks file. Name is the stable
// identifier used for the persisted cursor and the hook:<name> actor, so
// renaming a hook starts a new cursor at the latest event.
type Config struct {
	Name    string   `json:"name"`
	Events  []string `json:"events,omitempty"`
	Command []string `json:"command,omitempty"`
	URL     string   `json:"url,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Load reads a JSON array of hook configs. Unknown fields, trailing data, and
// invalid entries are rejected.
func Load(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return nil, fmt.Errorf("hooks file %s: must be a JSON array", path)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var configs []Config
	if err := decoder.Decode(&configs); err != nil {
		return nil, fmt.Errorf("hooks file %s: %w", path, err)
	}
	if err := decoder.Decode(&json.RawMessage{}); err != io.EOF {
		return nil, fmt.Errorf("hooks file %s: unexpected data after JSON array", path)
	}
	if err := validate(configs); err != nil {
		return nil, fmt.Errorf("hooks file %s: %w", path, err)
	}
	return configs, nil
}

func validate(configs []Config) error {
	seen := make(map[string]bool, len(configs))
	for index, config := range configs {
		if err := validateOne(config); err != nil {
			return fmt.Errorf("hook %d: %w", index, err)
		}
		if seen[config.Name] {
			return fmt.Errorf("hook %d: duplicate name %q", index, config.Name)
		}
		seen[config.Name] = true
	}
	return nil
}

func validateOne(config Config) error {
	if !namePattern.MatchString(config.Name) {
		return fmt.Errorf("invalid name %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", config.Name)
	}
	for _, event := range config.Events {
		if event == "" {
			return fmt.Errorf("hook %q: empty event type", config.Name)
		}
	}
	hasCommand, hasURL := config.Command != nil, config.URL != ""
	if hasCommand == hasURL {
		return fmt.Errorf("hook %q: exactly one of command or url is required", config.Name)
	}
	if hasCommand {
		if len(config.Command) == 0 || config.Command[0] == "" {
			return fmt.Errorf("hook %q: command must be a non-empty argv array", config.Name)
		}
		return nil
	}
	return validateURL(config)
}

// validateURL never includes the configured URL or parser details in its
// errors, since the URL may carry a token.
func validateURL(config Config) error {
	target, err := url.Parse(config.URL)
	if err != nil {
		return fmt.Errorf("hook %q: url is not a valid URL", config.Name)
	}
	switch {
	case target.Scheme != "http" && target.Scheme != "https":
		return fmt.Errorf("hook %q: url scheme must be http or https", config.Name)
	case target.Hostname() == "":
		return fmt.Errorf("hook %q: url must include a host", config.Name)
	case strings.HasSuffix(target.Host, ":"):
		return fmt.Errorf("hook %q: url port must not be empty", config.Name)
	case target.User != nil:
		return fmt.Errorf("hook %q: url must not contain credentials", config.Name)
	}
	return nil
}
