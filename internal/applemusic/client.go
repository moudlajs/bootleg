// Package applemusic is a minimal client for the Apple Music web player's private API.
package applemusic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// DefaultBaseURL is the API host the web player talks to.
const DefaultBaseURL = "https://amp-api.music.apple.com"

// origin is sent on every request; the API rejects requests without it.
const origin = "https://music.apple.com"

var (
	// ErrUnauthorized means the API returned 401 or 403: the web-player tokens were rejected.
	ErrUnauthorized = errors.New("unauthorized: the Apple Music web-player tokens were rejected")
	// ErrRateLimited means the API returned 429.
	ErrRateLimited = errors.New("rate limited by Apple Music")
	// ErrNotFound means the API returned 404.
	ErrNotFound = errors.New("not found")
)

// Client calls the Apple Music API; it is safe to reuse.
type Client struct {
	http      *http.Client
	baseURL   string
	devToken  string
	userToken string
}

// New returns a Client for baseURL (DefaultBaseURL in production).
func New(httpClient *http.Client, baseURL, devToken, userToken string) *Client {
	return &Client{
		http:      httpClient,
		baseURL:   baseURL,
		devToken:  devToken,
		userToken: userToken,
	}
}

// String redacts the tokens, which fmt would otherwise print via unexported fields.
func (c *Client) String() string {
	return fmt.Sprintf("applemusic.Client{baseURL:%s tokens:<redacted>}", c.baseURL)
}

// GoString covers %#v, which bypasses String.
func (c *Client) GoString() string { return c.String() }

// LogValue implements slog.LogValuer, so slog.Any("client", c) is safe.
func (c *Client) LogValue() slog.Value {
	return slog.GroupValue(slog.String("base_url", c.baseURL), slog.String("tokens", "<redacted>"))
}

// do sends one request; its errors never include headers, so they can't leak tokens.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s body: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.devToken)
	req.Header.Set("Media-User-Token", c.userToken)
	req.Header.Set("Origin", origin)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s %s: status %d: %w", method, path, resp.StatusCode, ErrUnauthorized)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%s %s: %w", method, path, ErrRateLimited)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("%s %s: unexpected status %d", method, path, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}
