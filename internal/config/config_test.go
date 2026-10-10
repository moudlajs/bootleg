package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unsetEnv unsets key for this test; t.Setenv's cleanup restores it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDotEnv(t *testing.T) {
	tests := []struct {
		name    string
		content string
		preset  map[string]string // set in the environment before loading
		want    map[string]string // expected afterwards
	}{
		{
			name:    "plain values",
			content: "AMTEST_A=one\nAMTEST_B=two\n",
			want:    map[string]string{"AMTEST_A": "one", "AMTEST_B": "two"},
		},
		{
			name:    "comments, blanks, export prefix, whitespace",
			content: "# comment\n\n  export AMTEST_A = one  \n",
			want:    map[string]string{"AMTEST_A": "one"},
		},
		{
			name:    "quotes are stripped",
			content: "AMTEST_A=\"double\"\nAMTEST_B='single'\nAMTEST_C=\"mismatched'\n",
			want:    map[string]string{"AMTEST_A": "double", "AMTEST_B": "single", "AMTEST_C": "\"mismatched'"},
		},
		{
			name:    "value containing = is kept whole",
			content: "AMTEST_A=abc==\n",
			want:    map[string]string{"AMTEST_A": "abc=="},
		},
		{
			name:    "existing env wins",
			content: "AMTEST_A=from-file\nAMTEST_B=from-file\n",
			preset:  map[string]string{"AMTEST_A": "from-shell"},
			want:    map[string]string{"AMTEST_A": "from-shell", "AMTEST_B": "from-file"},
		},
		{
			name:    "explicitly empty env still wins",
			content: "AMTEST_A=from-file\n",
			preset:  map[string]string{"AMTEST_A": ""},
			want:    map[string]string{"AMTEST_A": ""},
		},
		{
			name:    "empty file",
			content: "",
			want:    map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"AMTEST_A", "AMTEST_B", "AMTEST_C"} {
				unsetEnv(t, k)
			}
			for k, v := range tt.preset {
				t.Setenv(k, v)
			}

			if err := LoadDotEnv(writeFile(t, tt.content)); err != nil {
				t.Fatalf("LoadDotEnv() error = %v", err)
			}
			for k, want := range tt.want {
				if got := os.Getenv(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("LoadDotEnv() error = %v, want nil", err)
	}
}

func TestLoadDotEnvMalformedLineDoesNotEchoContent(t *testing.T) {
	unsetEnv(t, "AMTEST_A")
	secret := "eyJhbGciOiJFUzI1NiJ9.not-a-real-token"
	err := LoadDotEnv(writeFile(t, "AMTEST_A=ok\n"+secret+"\n"))
	if err == nil {
		t.Fatal("LoadDotEnv() error = nil, want error")
	}
	if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("error %q should name line 2", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q leaks the line content", err)
	}
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		want        Config
		wantMissing string // variable name expected in the error, "" for success
	}{
		{
			name: "all set",
			env:  map[string]string{EnvDevToken: "dev", EnvUserToken: "user", EnvStorefront: "cz"},
			want: Config{DevToken: "dev", UserToken: "user", Storefront: "cz"},
		},
		{
			name: "storefront defaults, Bearer prefix and whitespace stripped",
			env:  map[string]string{EnvDevToken: " Bearer dev\n", EnvUserToken: "user "},
			want: Config{DevToken: "dev", UserToken: "user", Storefront: DefaultStorefront},
		},
		{
			name:        "dev token missing",
			env:         map[string]string{EnvUserToken: "user"},
			wantMissing: EnvDevToken,
		},
		{
			name:        "user token blank",
			env:         map[string]string{EnvDevToken: "dev", EnvUserToken: "   "},
			wantMissing: EnvUserToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{EnvDevToken, EnvUserToken, EnvStorefront} {
				unsetEnv(t, k)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := FromEnv()
			if tt.wantMissing != "" {
				if !errors.Is(err, ErrMissingToken) {
					t.Fatalf("FromEnv() error = %v, want ErrMissingToken", err)
				}
				if !strings.Contains(err.Error(), tt.wantMissing) {
					t.Errorf("error %q should name %s", err, tt.wantMissing)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromEnv() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("FromEnv() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestConfigNeverPrintsTokens(t *testing.T) {
	c := Config{DevToken: "SECRET-DEV", UserToken: "SECRET-USER", Storefront: "cz"}

	var logBuf bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuf, nil)).Info("loaded", "config", c)

	outputs := map[string]string{
		"%v":     fmt.Sprintf("%v", c),
		"String": c.String(),
		"%+v":    fmt.Sprintf("%+v", c),
		"%#v":    fmt.Sprintf("%#v", c),
		"json":   mustJSON(t, c),
		"slog":   logBuf.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, "SECRET") {
			t.Errorf("%s output leaks a token: %s", name, out)
		}
		if !strings.Contains(out, "cz") {
			t.Errorf("%s output lost the storefront: %s", name, out)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStripBearer(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Bearer eyJabc", "eyJabc"},
		{"BearereyJabc", "eyJabc"}, // space lost while copying
		{"bearer\teyJabc", "eyJabc"},
		{"BEARER  eyJabc", "eyJabc"},
		{"Bearer\u00a0eyJabc", "eyJabc"}, // non-breaking space from a web page
		{"eyJabc", "eyJabc"},
		{"Bear", "Bear"},
		{"Bearer", ""}, // then FromEnv reports the token as missing
		{"", ""},
	}
	for _, tt := range tests {
		if got := stripBearer(tt.in); got != tt.want {
			t.Errorf("stripBearer(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFromEnvBearerOnlyIsMissing(t *testing.T) {
	unsetEnv(t, EnvStorefront)
	t.Setenv(EnvDevToken, "Bearer ")
	t.Setenv(EnvUserToken, "user")
	if _, err := FromEnv(); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("FromEnv() error = %v, want ErrMissingToken", err)
	}
}

func TestDevTokenExpires(t *testing.T) {
	jwt := func(payload string) string {
		return "eyJhbGciOiJFUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	}
	tests := []struct {
		name  string
		token string
		want  time.Time
		ok    bool
	}{
		{"exp", jwt(`{"iss":"x","exp":1796860800}`), time.Unix(1796860800, 0).UTC(), true},
		{"padded payload", strings.Replace(jwt(`{"exp":1796860800}`), ".sig", "", 1) + "==.sig", time.Unix(1796860800, 0).UTC(), true},
		{"no exp", jwt(`{"iss":"x"}`), time.Time{}, false},
		{"not a JWT", "abc", time.Time{}, false},
		{"bad base64", "a.!!!.c", time.Time{}, false},
		{"bad JSON", jwt(`{`), time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Config{DevToken: tt.token}.DevTokenExpires()
			if ok != tt.ok || !got.Equal(tt.want) {
				t.Errorf("DevTokenExpires() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
