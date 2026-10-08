// Package auth is bootleg's own small OAuth 2.1 authorization server, so
// that only the owner can use the hosted connector, which holds the owner's
// Apple Music tokens. It knows OAuth, not MCP or Apple Music.
//
// Ported from moudlajs/waiverwatch (c044bbd), where it ran in production
// before that project moved to multi-user sign-in.
//
// Claude identifies itself with a Client ID Metadata Document (its client_id
// is an HTTPS URL); only Claude's documents are accepted. The owner signs in
// once per client with a passphrase. Codes and tokens are HMAC-signed and
// self-contained, so nothing is stored and restarts don't sign anyone out.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Claude's published client identities.
const (
	ClaudeClient     = "https://claude.ai/oauth/mcp-oauth-client-metadata"   // claude.ai, desktop and mobile apps
	ClaudeCodeClient = "https://claude.ai/oauth/claude-code-client-metadata" // Claude Code
)

const (
	codeTTL    = 2 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 90 * 24 * time.Hour
	scope      = "mcp"
)

// Config configures a Server.
type Config struct {
	BaseURL    string   // public origin, e.g. https://bootleg-xyz.a.run.app
	Passphrase string   // what the owner types to sign in; at least 12 characters
	SigningKey []byte   // HMAC key for codes and tokens; at least 32 bytes
	Clients    []string // accepted client_id URLs; default Claude's two
	HTTPClient *http.Client
}

// Server serves the OAuth endpoints and guards the MCP endpoint.
type Server struct {
	base       string
	passHash   [32]byte
	signer     signer
	clients    map[string]bool
	httpClient *http.Client
	now        func() time.Time

	// Passphrase attempts across all clients: slow enough that guessing a
	// 12+ character passphrase is hopeless, fast enough for typos.
	logins *rate.Limiter

	mu        sync.Mutex
	usedCodes map[string]time.Time // single-use codes, kept until they expire
	docs      map[string]cachedDoc // client metadata documents
}

// New checks cfg and returns a Server.
func New(cfg Config) (*Server, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !isLoopback(u)) {
		return nil, fmt.Errorf("base URL %q must be an https origin", cfg.BaseURL)
	}
	if len(cfg.Passphrase) < 12 {
		return nil, errors.New("passphrase must be at least 12 characters")
	}
	if len(cfg.SigningKey) < 32 {
		return nil, errors.New("signing key must be at least 32 bytes")
	}
	clients := cfg.Clients
	if len(clients) == 0 {
		clients = []string{ClaudeClient, ClaudeCodeClient}
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	s := &Server{
		base:       base,
		passHash:   sha256.Sum256([]byte(cfg.Passphrase)),
		signer:     signer{key: cfg.SigningKey},
		clients:    make(map[string]bool),
		httpClient: hc,
		now:        time.Now,
		logins:     rate.NewLimiter(rate.Every(10*time.Second), 5),
		usedCodes:  make(map[string]time.Time),
		docs:       make(map[string]cachedDoc),
	}
	for _, c := range clients {
		s.clients[c] = true
	}
	return s, nil
}

// Resource is the protected MCP endpoint's URL, as the owner adds it in Claude.
func (s *Server) Resource() string { return s.base + "/mcp" }

func (s *Server) resourceMetadataURL() string {
	return s.base + "/.well-known/oauth-protected-resource/mcp"
}

// Routes registers the discovery documents and the OAuth endpoints.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authorizationServer)
	mux.HandleFunc("GET /authorize", s.authorizeForm)
	mux.HandleFunc("POST /authorize", s.authorizeSubmit)
	mux.HandleFunc("POST /token", s.token)
}

// Protect lets requests with a valid access token for Resource through, and
// answers the rest with the 401 challenge that starts Claude's sign-in.
func (s *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok {
			if c, err := s.signer.verify(token, kindAccess, s.now()); err == nil && c.Audience == s.Resource() {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer error="invalid_token", error_description="sign in to bootleg", resource_metadata=%q, scope=%q`,
			s.resourceMetadataURL(), scope))
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid_token", "error_description": "sign in to bootleg",
		})
	})
}

// useCode marks a code ID as spent. It reports false if it already was.
func (s *Server) useCode(id string, expires time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, exp := range s.usedCodes {
		if now.After(exp) {
			delete(s.usedCodes, k)
		}
	}
	if _, used := s.usedCodes[id]; used {
		return false
	}
	s.usedCodes[id] = expires
	return true
}

func (s *Server) passphraseOK(given string) bool {
	h := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(h[:], s.passHash[:]) == 1
}

// pkceOK checks an S256 code verifier against its challenge.
func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}
