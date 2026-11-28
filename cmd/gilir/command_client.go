package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCommandURL = "http://" + defaultHost + ":" + defaultPort
	apiCommandPath    = "/api/v1/command"
	// commandDialTimeout bounds reaching a service; commandTimeout must allow
	// server work such as SQLite's busy timeout and Git during `repo add`.
	commandDialTimeout = 5 * time.Second
	commandTimeout     = 60 * time.Second
)

// commandClient posts one command at a time. It never follows redirects and
// never uses an environment proxy, so GILIR_URL always names the process the
// request reaches. A failed mutation is not retried: the outcome may be
// unknown, and retrying could duplicate it.
var commandClient = &http.Client{
	Transport: func() *http.Transport {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = (&net.Dialer{Timeout: commandDialTimeout}).DialContext
		return transport
	}(),
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type commandRequest struct {
	Command string         `json:"command"`
	Actor   string         `json:"actor"`
	Args    map[string]any `json:"args"`
}

type commandResponse struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// postCommand sends one wire command to the service named by GILIR_URL and
// returns its data. The raw data is handed to the CLI renderer unchanged so
// --json output and arbitrary integer IDs keep their exact spelling.
//
// GILIR_URL is the only client endpoint setting. It deliberately does not
// consult service state or GILIR_DB: a client may reach a service on another
// host, through an SSH tunnel, or with a different database path.
func postCommand(ctx context.Context, command, actor string, args map[string]any) (json.RawMessage, error) {
	endpoint, err := commandEndpoint(envOr("GILIR_URL", defaultCommandURL))
	if err != nil {
		return nil, err
	}
	return sendCommand(ctx, endpoint, command, actor, args)
}

// sendCommand posts one command to an already validated endpoint.
func sendCommand(ctx context.Context, endpoint *url.URL, command, actor string, args map[string]any) (json.RawMessage, error) {
	origin := endpoint.Scheme + "://" + endpoint.Host

	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(commandRequest{Command: command, Actor: actor, Args: args})
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", command, err)
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build %s request for Gilir service at %s: %w", command, origin, err)
	}
	request.Header.Set("Content-Type", "application/json")
	// Without GetBody the transport cannot replay a POST that may already
	// have been sent, including on an HTTP/2 REFUSED_STREAM or GOAWAY.
	request.GetBody = nil

	response, err := commandClient.Do(request)
	if err != nil {
		cause := err
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			cause = urlErr.Err
		}
		message := fmt.Sprintf("cannot reach Gilir service at %s: %v; start it with \"gilir service start\" or check GILIR_URL and your SSH tunnel", origin, cause)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
			message += "; a mutation may have been applied, so check the service before retrying"
		}
		return nil, errors.New(message)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var envelope commandResponse
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("Gilir service at %s returned HTTP %d without a valid command response", origin, response.StatusCode)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("Gilir service at %s returned HTTP %d with trailing data after the command response", origin, response.StatusCode)
	}
	if response.StatusCode == http.StatusOK && envelope.Error == nil {
		if len(envelope.Data) == 0 {
			return nil, fmt.Errorf("Gilir service at %s returned HTTP 200 without command data", origin)
		}
		return envelope.Data, nil
	}
	if envelope.Error != nil && envelope.Error.Message != "" {
		return nil, errors.New(envelope.Error.Message)
	}
	message := http.StatusText(response.StatusCode)
	if message == "" {
		message = "command failed"
	}
	return nil, fmt.Errorf("Gilir service at %s returned HTTP %d: %s", origin, response.StatusCode, message)
}

// commandEndpoint validates one GILIR_URL value and returns the command URL.
// The value must be an http or https origin with an optional trailing slash.
// Nothing from the value is included in validation errors, since a malformed
// URL may still carry credentials.
func commandEndpoint(value string) (*url.URL, error) {
	if value == "" {
		value = defaultCommandURL
	}
	if strings.TrimSpace(value) != value {
		return nil, invalidEndpoint("it must not contain surrounding whitespace")
	}
	if strings.ContainsRune(value, '#') {
		return nil, invalidEndpoint("it must not contain a fragment")
	}
	target, err := url.Parse(value)
	if err != nil {
		return nil, invalidEndpoint("it is not a valid URL")
	}
	switch {
	case target.Scheme != "http" && target.Scheme != "https":
		return nil, invalidEndpoint("it must start with http:// or https://")
	case target.Opaque != "":
		return nil, invalidEndpoint("it must be an http:// or https:// origin")
	case target.User != nil:
		return nil, invalidEndpoint("it must not contain credentials")
	case target.Host == "" || target.Hostname() == "":
		return nil, invalidEndpoint("it must include a host")
	case strings.HasSuffix(target.Host, ":"):
		return nil, invalidEndpoint("its port must not be empty")
	case target.RawQuery != "" || target.ForceQuery:
		return nil, invalidEndpoint("it must not contain a query")
	case target.Path != "" && target.Path != "/":
		return nil, invalidEndpoint("it must not contain a path")
	}
	// url.Parse reads the last colon in an unbracketed IPv6 literal as a
	// port. Requiring brackets keeps the authority and Host header honest.
	if strings.Contains(target.Hostname(), ":") && !strings.HasPrefix(target.Host, "[") {
		return nil, invalidEndpoint("an IPv6 address must be enclosed in brackets")
	}
	if port := target.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, invalidEndpoint("its port must be a number from 1 to 65535")
		}
		// The service compares Host ports literally, so a padded port would
		// be rejected after it passed client validation.
		if port != strconv.Itoa(n) {
			return nil, invalidEndpoint("its port must be written without leading zeros")
		}
	}
	endpoint := *target
	endpoint.Path = apiCommandPath
	endpoint.RawPath = ""
	endpoint.Fragment = ""
	endpoint.RawFragment = ""
	return &endpoint, nil
}

func invalidEndpoint(reason string) error {
	return fmt.Errorf("invalid GILIR_URL: %s (expected an http:// or https:// origin, for example %s)", reason, defaultCommandURL)
}
