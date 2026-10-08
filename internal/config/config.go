// Package config reads the Apple Music tokens from the environment, with a
// small .env loader so users don't have to export them by hand.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"unicode"
)

// Environment variable names.
const (
	EnvDevToken   = "AM_DEV_TOKEN"  // #nosec G101 -- a variable name, not a credential.
	EnvUserToken  = "AM_USER_TOKEN" // #nosec G101 -- a variable name, not a credential.
	EnvStorefront = "AM_STOREFRONT"
)

// DefaultStorefront is used when neither AM_STOREFRONT nor -storefront is set.
const DefaultStorefront = "us"

// ErrMissingToken is returned by FromEnv when a required token is unset.
var ErrMissingToken = errors.New("missing token")

// Config holds everything read from the environment; its tokens are never printed or logged.
type Config struct {
	DevToken   string `json:"-"`
	UserToken  string `json:"-"`
	Storefront string `json:"storefront"`
}

// FromEnv reads the tokens and storefront from the process environment.
func FromEnv() (Config, error) {
	c := Config{
		DevToken:   strings.TrimSpace(os.Getenv(EnvDevToken)),
		UserToken:  strings.TrimSpace(os.Getenv(EnvUserToken)),
		Storefront: strings.TrimSpace(os.Getenv(EnvStorefront)),
	}
	// Users often paste the whole header value, sometimes without the space after "Bearer".
	c.DevToken = stripBearer(c.DevToken)

	for _, v := range []struct{ name, val string }{
		{EnvDevToken, c.DevToken},
		{EnvUserToken, c.UserToken},
	} {
		if v.val == "" {
			return Config{}, fmt.Errorf("%w: %s is not set (see .env.example)", ErrMissingToken, v.name)
		}
	}
	if c.Storefront == "" {
		c.Storefront = DefaultStorefront
	}
	return c, nil
}

// stripBearer is safe because the dev token is a JWT, which always starts with "eyJ".
func stripBearer(s string) string {
	const prefix = "bearer"
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return strings.TrimLeftFunc(s[len(prefix):], unicode.IsSpace)
	}
	return s
}

// String redacts the tokens, so printing a Config with %v or %s is safe.
func (c Config) String() string {
	return fmt.Sprintf("Config{DevToken:%s UserToken:%s Storefront:%s}",
		redact(c.DevToken), redact(c.UserToken), c.Storefront)
}

// GoString covers %#v, which bypasses String.
func (c Config) GoString() string { return c.String() }

// LogValue implements slog.LogValuer, so slog.Any("config", c) is safe too.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("dev_token", redact(c.DevToken)),
		slog.String("user_token", redact(c.UserToken)),
		slog.String("storefront", c.Storefront),
	)
}

func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}

// LoadDotEnv sets unset variables from KEY=value lines in path; a missing file is fine.
func LoadDotEnv(path string) error {
	f, err := os.Open(path) // #nosec G304 -- the path is chosen by the user running the tool.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			// Report the line number only: the line itself may hold a token.
			return fmt.Errorf("%s:%d: expected KEY=value", path, n)
		}
		key = strings.TrimSpace(key)
		val = unquote(strings.TrimSpace(val))

		// An explicit empty value in the shell still counts as set.
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, n, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
