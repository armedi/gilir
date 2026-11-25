package opencode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	initialRetryDelay = 2 * time.Second
	maxRetryDelay     = 10 * time.Second
	maxSecrets        = 8
)

// Overrides are explicitly configured connection values. A nil field is
// discovered from the managed OpenCode service. A pointer to an empty string
// is a deliberate empty override, not a request to discover.
type Overrides struct {
	URL      *string
	Password *string
}

// Connector resolves, caches and refreshes the client for one OpenCode
// server. Discovered values are re-resolved only after a failure and with
// backoff, so a stopped service cannot spawn a CLI process on every watcher
// tick. Explicit overrides are never replaced.
type Connector struct {
	discovery Discovery
	url       *string
	password  *string

	// now is swappable in tests.
	now func() time.Time

	mu           sync.Mutex
	client       *Client
	serverURL    string
	lastURL      string
	lastPassword string
	secrets      []string
	stale        bool
	delay        time.Duration
	retryAt      time.Time
	lastErr      error
	authFailures int
}

// NewConnector returns a connector. An explicit URL is validated immediately
// so a bad GILIR_OPENCODE_URL fails startup instead of every tick.
func NewConnector(discovery Discovery, overrides Overrides) (*Connector, error) {
	c := &Connector{discovery: discovery, now: time.Now, stale: true}
	if overrides.URL != nil {
		value := *overrides.URL
		if _, err := New(value, ""); err != nil {
			return nil, err
		}
		c.url = &value
	}
	if overrides.Password != nil {
		value := *overrides.Password
		c.password = &value
		c.addSecretLocked(value)
	}
	if discovery == nil && (c.url == nil || c.password == nil) {
		return nil, errors.New("opencode: discovery is required when the server URL or password is not set")
	}
	return c, nil
}

// Client returns the current client and its canonical server URL. It runs
// discovery when a value is missing or stale, honoring the retry backoff after
// a failure. While backing off it keeps returning the last client, so a
// keep-alive connection can recover without another CLI invocation.
func (c *Connector) Client(ctx context.Context) (*Client, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stale {
		return c.client, c.serverURL, nil
	}
	if c.now().Before(c.retryAt) {
		if c.client != nil {
			return c.client, c.serverURL, nil
		}
		return nil, "", c.lastErr
	}
	client, serverURL, err := c.resolveLocked(ctx)
	if err != nil {
		c.lastErr = err
		c.client, c.serverURL = nil, ""
		c.backoffLocked()
		c.retryAt = c.now().Add(c.delay)
		return nil, "", err
	}
	c.client, c.serverURL = client, serverURL
	c.stale = false
	c.retryAt = time.Time{}
	c.lastErr = nil
	return client, serverURL, nil
}

// Failed reports that err came from the client returned by Client and applies
// the refresh policy for it. It returns true when new credentials were made
// available, so the caller should retry at once.
//
// A 401 with a discovered password marks the client stale for an immediate
// re-read of the managed service credentials. Repeated 401s (a password that
// did not actually rotate) fall back to the backoff so the CLI is not run
// twice per tick. A transport failure only schedules a later re-discovery; the
// existing client keeps retrying on its own.
func (c *Connector) Failed(err error) bool {
	if err == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stale {
		// This error came from a refresh that is already scheduled, so
		// re-arming the policy would push the retry window forward on every
		// tick and it would never fire.
		return false
	}
	switch {
	case IsUnauthorized(err):
		if c.password != nil {
			return false // explicit password; re-discovery cannot help
		}
		c.client, c.serverURL = nil, ""
		c.stale = true
		c.lastErr = err
		c.authFailures++
		if c.authFailures > 1 {
			// The re-discovered password was refused too. Fall back to the
			// backoff so a stuck service cannot run the CLI twice per tick.
			c.backoffLocked()
			c.retryAt = c.now().Add(c.delay)
			return false
		}
		c.retryAt = time.Time{}
		return true
	case IsUnreachable(err):
		if c.url != nil && c.password != nil {
			return false // fixed endpoint; the keep-alive client recovers
		}
		c.stale = true
		c.lastErr = err
		c.backoffLocked()
		c.retryAt = c.now().Add(c.delay)
		return false
	}
	return false
}

// Redact removes every password this connector has seen from text.
func (c *Connector) Redact(text string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, secret := range c.secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func (c *Connector) resolveLocked(ctx context.Context) (*Client, string, error) {
	serverURL := ""
	if c.url != nil {
		serverURL = *c.url
	} else {
		discovered, err := c.discovery.URL(ctx)
		if err != nil {
			return nil, "", err
		}
		serverURL = discovered
	}
	password := ""
	if c.password != nil {
		password = *c.password
	} else {
		discovered, err := c.discovery.Password(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("opencode: reading the managed service password: %w", err)
		}
		password = discovered
	}
	client, err := New(serverURL, password)
	if err != nil {
		return nil, "", fmt.Errorf("opencode: resolved server address: %w", err)
	}
	if serverURL != c.lastURL || password != c.lastPassword {
		// A new endpoint or password (the service started, or rotated its
		// password): start the retry cycle over so recovery is prompt.
		c.lastURL, c.lastPassword = serverURL, password
		c.authFailures = 0
		c.delay = 0
	}
	c.addSecretLocked(password)
	return client, client.ServerURL(), nil
}

func (c *Connector) backoffLocked() {
	switch {
	case c.delay <= 0:
		c.delay = initialRetryDelay
	case c.delay < maxRetryDelay:
		c.delay = min(c.delay*2, maxRetryDelay)
	}
}

func (c *Connector) addSecretLocked(secret string) {
	if secret == "" {
		return
	}
	for _, known := range c.secrets {
		if known == secret {
			return
		}
	}
	if len(c.secrets) == maxSecrets {
		c.secrets = c.secrets[len(c.secrets)-maxSecrets+1:]
	}
	c.secrets = append(c.secrets, secret)
}
