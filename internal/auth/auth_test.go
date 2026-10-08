package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	pass     = "correct horse battery"
	verifier = "a-perfectly-random-pkce-verifier-that-is-long-enough-0123456789"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// setup runs a fake client metadata host and the auth server behind a mux
// with a protected /mcp. It returns the auth server's URL, the client_id,
// and the server.
func setup(t *testing.T) (string, string, *Server) {
	t.Helper()
	var clientID string
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     clientID,
			"client_name":   "Claude",
			"redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback", "http://localhost/callback"},
		})
	}))
	t.Cleanup(meta.Close)
	clientID = meta.URL + "/oauth/client"

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s, err := New(Config{BaseURL: srv.URL, Passphrase: pass, SigningKey: key, Clients: []string{clientID}})
	if err != nil {
		t.Fatal(err)
	}
	s.Routes(mux)
	mux.Handle("/mcp", s.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tools"))
	})))
	return srv.URL, clientID, s
}

func authorizeParams(clientID string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "state": {"xyz"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {"mcp"},
	}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// signIn posts the passphrase and returns the status and, on success, where
// it redirects.
func signIn(t *testing.T, base string, form url.Values) (int, *url.URL) {
	t.Helper()
	resp, err := noRedirect.PostForm(base+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	return resp.StatusCode, loc
}

func tokenRequest(t *testing.T, base string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(base+"/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func mcpStatus(t *testing.T, base, bearer string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate")
}

func TestFullFlow(t *testing.T) {
	base, clientID, _ := setup(t)

	// 1. Unauthenticated MCP request: 401 pointing at the metadata.
	status, challengeHeader := mcpStatus(t, base, "")
	if status != http.StatusUnauthorized || !strings.Contains(challengeHeader, `resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("unauthenticated: %d %q", status, challengeHeader)
	}

	// 2. Discovery documents.
	var prm, asm map[string]any
	for path, dst := range map[string]*map[string]any{
		"/.well-known/oauth-protected-resource/mcp": &prm,
		"/.well-known/oauth-authorization-server":   &asm,
	} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(resp.Body).Decode(dst)
		_ = resp.Body.Close()
	}
	if prm["resource"] != base+"/mcp" || asm["issuer"] != base || asm["client_id_metadata_document_supported"] != true {
		t.Fatalf("metadata: %v / %v", prm, asm)
	}

	// 3. The sign-in page names the client's host.
	resp, err := http.Get(base + "/authorize?" + authorizeParams(clientID).Encode())
	if err != nil {
		t.Fatal(err)
	}
	var page strings.Builder
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	page.Write(buf[:n])
	_ = resp.Body.Close()
	host := strings.TrimPrefix(strings.Split(clientID, "/oauth")[0], "http://")
	if resp.StatusCode != http.StatusOK || !strings.Contains(page.String(), host) {
		t.Fatalf("authorize page: %d", resp.StatusCode)
	}

	// 4. Right passphrase: redirect back with code, state and iss.
	form := authorizeParams(clientID)
	form.Set("passphrase", pass)
	backStatus, loc := signIn(t, base, form)
	if backStatus != http.StatusFound || loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" || loc.Query().Get("iss") != base {
		t.Fatalf("redirect: %d %s", backStatus, loc)
	}
	code := loc.Query().Get("code")

	// 5. Code for tokens.
	exchange := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {verifier},
	}
	status, tokens := tokenRequest(t, base, exchange)
	if status != http.StatusOK || tokens["token_type"] != "Bearer" || tokens["refresh_token"] == nil {
		t.Fatalf("token: %d %v", status, tokens)
	}
	access := tokens["access_token"].(string)

	// 6. The code is single-use.
	if status, body := tokenRequest(t, base, exchange); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("code reuse: %d %v", status, body)
	}

	// 7. The access token opens /mcp; the refresh token does not.
	if status, _ := mcpStatus(t, base, access); status != http.StatusOK {
		t.Errorf("with access token: %d", status)
	}
	if status, _ := mcpStatus(t, base, tokens["refresh_token"].(string)); status != http.StatusUnauthorized {
		t.Errorf("refresh token used as bearer: %d", status)
	}

	// 8. Refresh.
	status, refreshed := tokenRequest(t, base, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)}, "client_id": {clientID},
	})
	if status != http.StatusOK || refreshed["access_token"] == nil {
		t.Errorf("refresh: %d %v", status, refreshed)
	}
}

func TestAuthorizeRejects(t *testing.T) {
	base, clientID, _ := setup(t)
	tests := []struct {
		name string
		edit func(url.Values)
	}{
		{"unknown client", func(v url.Values) { v.Set("client_id", "https://evil.example/client") }},
		{"unregistered redirect", func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") }},
		{"no PKCE", func(v url.Values) { v.Del("code_challenge") }},
		{"plain PKCE", func(v url.Values) { v.Set("code_challenge_method", "plain") }},
		{"wrong response type", func(v url.Values) { v.Set("response_type", "token") }},
		{"another resource", func(v url.Values) { v.Set("resource", "https://other.example/mcp") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := authorizeParams(clientID)
			tt.edit(v)
			v.Set("passphrase", pass)
			// Both the page and the submit refuse, and never redirect.
			resp, err := http.Get(base + "/authorize?" + v.Encode())
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if sub, _ := signIn(t, base, v); resp.StatusCode != http.StatusBadRequest || sub != http.StatusBadRequest {
				t.Errorf("GET %d, POST %d; want 400 and 400", resp.StatusCode, sub)
			}
		})
	}
}

func TestWrongPassphraseAndRateLimit(t *testing.T) {
	base, clientID, _ := setup(t)
	form := authorizeParams(clientID)
	form.Set("passphrase", "not the passphrase")
	var statuses []int
	for range 7 {
		status, _ := signIn(t, base, form)
		statuses = append(statuses, status)
	}
	for i, s := range statuses {
		want := http.StatusUnauthorized
		if i >= 5 {
			want = http.StatusTooManyRequests
		}
		if s != want {
			t.Errorf("attempt %d: %d, want %d (all: %v)", i+1, s, want, statuses)
		}
	}
}

func TestTokenRejects(t *testing.T) {
	base, clientID, s := setup(t)
	form := authorizeParams(clientID)
	form.Set("passphrase", pass)
	_, loc := signIn(t, base, form)
	code := loc.Query().Get("code")
	good := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {verifier},
	}
	tests := []struct {
		name string
		edit func(url.Values)
		want string
	}{
		{"wrong verifier", func(v url.Values) { v.Set("code_verifier", strings.Repeat("x", 50)) }, "invalid_grant"},
		{"other redirect", func(v url.Values) { v.Set("redirect_uri", "http://localhost/callback") }, "invalid_grant"},
		{"other client", func(v url.Values) { v.Set("client_id", "https://other.example/c") }, "invalid_grant"},
		{"forged code", func(v url.Values) { v.Set("code", code[:len(code)-2]+"AA") }, "invalid_grant"},
		{"password grant", func(v url.Values) { v.Set("grant_type", "password") }, "unsupported_grant_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := url.Values{}
			for k, vs := range good {
				v[k] = vs
			}
			tt.edit(v)
			if status, body := tokenRequest(t, base, v); status != http.StatusBadRequest || body["error"] != tt.want {
				t.Errorf("%d %v, want 400 %s", status, body, tt.want)
			}
		})
	}

	// The untouched request still works: none of the failures spent the code.
	if status, _ := tokenRequest(t, base, good); status != http.StatusOK {
		t.Errorf("good exchange after failures: %d", status)
	}

	// Codes expire.
	_, loc = signIn(t, base, form)
	good.Set("code", loc.Query().Get("code"))
	s.now = func() time.Time { return time.Now().Add(codeTTL + time.Second) }
	if status, body := tokenRequest(t, base, good); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("expired code: %d %v", status, body)
	}
}

func TestAccessTokenScope(t *testing.T) {
	base, _, s := setup(t)
	now := time.Now()
	tok := func(c claims) string { return s.signer.sign(c) }
	tests := []struct {
		name   string
		bearer string
		want   int
	}{
		{"valid", tok(claims{Kind: kindAccess, Audience: s.Resource(), Expires: now.Add(time.Hour).Unix()}), http.StatusOK},
		{"expired", tok(claims{Kind: kindAccess, Audience: s.Resource(), Expires: now.Add(-time.Second).Unix()}), http.StatusUnauthorized},
		{"other audience", tok(claims{Kind: kindAccess, Audience: "https://other.example/mcp", Expires: now.Add(time.Hour).Unix()}), http.StatusUnauthorized},
		{"a code", tok(claims{Kind: kindCode, Audience: s.Resource(), Expires: now.Add(time.Hour).Unix()}), http.StatusUnauthorized},
		{"other key", signer{key: []byte(strings.Repeat("k", 32))}.sign(claims{Kind: kindAccess, Audience: s.Resource(), Expires: now.Add(time.Hour).Unix()}), http.StatusUnauthorized},
		{"garbage", "not-a-token", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, _ := mcpStatus(t, base, tt.bearer); status != tt.want {
				t.Errorf("%d, want %d", status, tt.want)
			}
		})
	}
}

func TestRedirectAllowed(t *testing.T) {
	registered := []string{"https://claude.ai/api/mcp/auth_callback", "http://localhost/callback", "http://127.0.0.1/callback"}
	tests := []struct {
		uri  string
		want bool
	}{
		{"https://claude.ai/api/mcp/auth_callback", true},
		{"http://localhost:3118/callback", true}, // Claude Code's ephemeral port
		{"http://127.0.0.1:50000/callback", true},
		{"http://localhost:3118/other", false},
		{"https://claude.ai:8443/api/mcp/auth_callback", false}, // ports only float for loopback
		{"http://evil.example/callback", false},
		{"http://[::1]:1234/callback", false}, // not registered
	}
	for _, tt := range tests {
		if got := redirectAllowed(tt.uri, registered); got != tt.want {
			t.Errorf("redirectAllowed(%q) = %v, want %v", tt.uri, got, tt.want)
		}
	}
}

func TestNewValidates(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"plain http", Config{BaseURL: "http://example.com", Passphrase: pass, SigningKey: key}},
		{"no base", Config{Passphrase: pass, SigningKey: key}},
		{"short passphrase", Config{BaseURL: "https://example.com", Passphrase: "short", SigningKey: key}},
		{"short key", Config{BaseURL: "https://example.com", Passphrase: pass, SigningKey: []byte("k")}},
	}
	for _, tt := range tests {
		if _, err := New(tt.cfg); err == nil {
			t.Errorf("%s: want an error", tt.name)
		}
	}
	if _, err := New(Config{BaseURL: "https://example.com/", Passphrase: pass, SigningKey: key}); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestSignInPageAllowsTheWayBack(t *testing.T) {
	tests := []struct {
		redirect, want string
	}{
		{"https://claude.ai/api/mcp/auth_callback", "form-action 'self' https://claude.ai"},
		{"http://localhost:3118/callback", "form-action 'self' http://localhost:3118"},
		{"", "form-action 'self'"}, // error pages: no redirect to allow
	}
	for _, tt := range tests {
		got := csp(tt.redirect)
		if !strings.HasSuffix(got, tt.want) || !strings.HasPrefix(got, "default-src 'none'") {
			t.Errorf("csp(%q) = %q, want it to end with %q", tt.redirect, got, tt.want)
		}
	}

	// And the real page sends it.
	base, clientID, _ := setup(t)
	resp, err := http.Get(base + "/authorize?" + authorizeParams(clientID).Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "form-action 'self' https://claude.ai") {
		t.Errorf("sign-in page CSP = %q, must allow redirecting to https://claude.ai", got)
	}
}
