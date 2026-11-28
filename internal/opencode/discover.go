package opencode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ErrServiceStopped reports that the managed OpenCode service is not running.
// Gilir never starts it on its own.
var ErrServiceStopped = errors.New("opencode: the managed service is not running")

const (
	discoverTimeout = 10 * time.Second
	maxCLIOutput    = 64 << 10
)

// Discovery resolves the local managed OpenCode service. CLI implements it
// with the opencode command line; tests substitute fakes.
type Discovery interface {
	// URL returns the server URL reported for the managed service, or
	// ErrServiceStopped when it is not running.
	URL(ctx context.Context) (string, error)
	// Password returns the Basic auth password of the managed service.
	Password(ctx context.Context) (string, error)
}

// CLI discovers the managed OpenCode service by running the opencode
// executable. It only reads service state; it never starts the service.
type CLI struct {
	// Path is the opencode executable. Empty means "opencode" from PATH.
	Path string
	// run overrides command execution in tests.
	run func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// NewCLI returns a CLI that runs "opencode" from PATH.
func NewCLI() *CLI { return &CLI{} }

// URL returns the URL reported by "opencode service status". The command
// prints the server URL when the service is running and "stopped" when it is
// not, both with exit status 0.
func (c *CLI) URL(ctx context.Context) (string, error) {
	stdout, err := c.exec(ctx, "service", "status")
	if err != nil {
		return "", err
	}
	return parseServiceStatus(stdout)
}

// Password returns the password printed by "opencode service get password".
func (c *CLI) Password(ctx context.Context) (string, error) {
	stdout, err := c.exec(ctx, "service", "get", "password")
	if err != nil {
		return "", err
	}
	return parseServicePassword(stdout)
}

func (c *CLI) exec(ctx context.Context, args ...string) ([]byte, error) {
	name := c.Path
	if name == "" {
		name = "opencode"
	}
	run := c.run
	if run == nil {
		run = runCLI
	}
	stdout, stderr, err := run(ctx, name, args...)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, errors.New("opencode: the opencode CLI is not installed: install OpenCode or set GILIR_OPENCODE_URL and OPENCODE_PASSWORD")
	case errors.Is(err, context.DeadlineExceeded):
		return nil, errors.New("opencode: the opencode CLI did not respond in time")
	case err != nil:
		detail := firstLine(stderr)
		if detail == "" {
			detail = firstLine(stdout)
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("opencode: opencode %s failed: %s", strings.Join(args, " "), truncate(detail, 200))
	}
	return stdout, nil
}

// runCLI runs one bounded opencode invocation with separate stdout and
// stderr capture.
func runCLI(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := &cappedBuffer{limit: maxCLIOutput}
	stderr := &cappedBuffer{limit: maxCLIOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// parseServiceStatus accepts the documented single-line output ("<url>" or
// "stopped") and stays tolerant of extra advisory lines before it.
func parseServiceStatus(out []byte) (string, error) {
	stopped := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if lower == "stopped" || strings.Contains(lower, "not running") {
			stopped = true
			continue
		}
		if u, err := url.Parse(line); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return line, nil
		}
	}
	if stopped {
		return "", ErrServiceStopped
	}
	return "", errors.New("opencode: unexpected output from opencode service status")
}

// parseServicePassword requires exactly one nonblank line so an advisory line
// can never be mistaken for the secret.
func parseServicePassword(out []byte) (string, error) {
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	switch len(lines) {
	case 0:
		return "", errors.New("opencode: opencode service get password printed no password")
	case 1:
		return lines[0], nil
	default:
		return "", errors.New("opencode: opencode service get password printed unexpected output")
	}
}

func firstLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Describe renders err for service status and startup logs. Known failures get
// an actionable message instead of a raw protocol error.
func Describe(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrServiceStopped):
		return `OpenCode is not running: start it with "opencode service start"`
	case IsUnauthorized(err):
		return "cannot authenticate to OpenCode: check OPENCODE_PASSWORD or the managed service password"
	case IsUnreachable(err):
		return "OpenCode is not reachable: the service may not be running"
	default:
		return err.Error()
	}
}
