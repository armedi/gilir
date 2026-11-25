package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	outputLimit   = 4 << 10
	responseLimit = 4 << 10
	waitDelay     = 5 * time.Second
)

// hiddenEnv names variables replaced or withheld from hook processes.
var hiddenEnv = map[string]bool{
	"GILIR_ACTOR":              true,
	"GILIR_EVENT_ID":           true,
	"OPENCODE_PASSWORD":        true,
	"OPENCODE_SERVER_PASSWORD": true,
}

// runCommand executes the hook argv directly (no shell) with the payload on
// stdin. Cancelling ctx kills the process; WaitDelay bounds how long Wait
// blocks on descendants that still hold the output pipes.
func runCommand(ctx context.Context, config Config, event Event) error {
	cmd := exec.CommandContext(ctx, config.Command[0], config.Command[1:]...)
	cmd.Stdin = bytes.NewReader(event.Payload)
	output := &cappedBuffer{limit: outputLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = waitDelay
	cmd.Env = commandEnv(config.Name, event.ID)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w: %w", ctx.Err(), err)
		}
		if text := strings.TrimSpace(output.String()); text != "" {
			return fmt.Errorf("command failed: %w: %s", err, text)
		}
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

func commandEnv(name string, eventID int64) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !hiddenEnv[key] {
			env = append(env, entry)
		}
	}
	return append(env, "GILIR_ACTOR=hook:"+name, "GILIR_EVENT_ID="+strconv.FormatInt(eventID, 10))
}

// cappedBuffer keeps the first limit bytes written and discards the rest
// while still reporting full writes, so the child never blocks on output.
type cappedBuffer struct {
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	if b.buf.Len()+len(p) > b.limit {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "...(truncated)"
	}
	return b.buf.String()
}

// webhookClient posts only to the configured endpoint: no environment proxy
// and no redirects.
var webhookClient = &http.Client{
	Transport: func() http.RoundTripper {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		return transport
	}(),
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func postEvent(ctx context.Context, endpoint string, event Event) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(event.Payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gilir-Event-ID", strconv.FormatInt(event.ID, 10))
	response, err := webhookClient.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			// Drop the URL from the message; it may carry a token in its query.
			err = urlErr.Err
		}
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, responseLimit))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("webhook returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
