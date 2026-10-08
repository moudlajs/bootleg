package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"
)

func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.Resource(),
		"authorization_servers":    []string{s.base},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{scope},
	})
}

func (s *Server) authorizationServer(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.base,
		"authorization_endpoint":                s.base + "/authorize",
		"token_endpoint":                        s.base + "/token",
		"scopes_supported":                      []string{scope},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none"}, // Claude is a public client
		"code_challenge_methods_supported":      []string{"S256"},
		"client_id_metadata_document_supported": true,
	})
}

type authRequest struct {
	ClientID, ClientHost, ClientName string
	RedirectURI, State, Challenge    string
	Scope, Resource                  string
}

// parseAuthorize's errors are shown on the page, never sent to an unverified redirect URI.
func (s *Server) parseAuthorize(ctx context.Context, v url.Values) (authRequest, error) {
	req := authRequest{
		ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"), State: v.Get("state"),
		Challenge: v.Get("code_challenge"), Scope: v.Get("scope"), Resource: v.Get("resource"),
	}
	if !s.clients[req.ClientID] {
		return req, fmt.Errorf("unknown client %q: only Claude can sign in here", req.ClientID)
	}
	doc, err := s.clientDoc(ctx, req.ClientID)
	if err != nil {
		return req, err
	}
	if !redirectAllowed(req.RedirectURI, doc.RedirectURIs) {
		return req, fmt.Errorf("redirect_uri %q is not registered for this client", req.RedirectURI)
	}
	switch {
	case v.Get("response_type") != "code":
		return req, errors.New("response_type must be code")
	case v.Get("code_challenge_method") != "S256" || req.Challenge == "":
		return req, errors.New("a PKCE S256 code_challenge is required")
	case req.Resource != "" && req.Resource != s.Resource():
		return req, fmt.Errorf("resource must be %s", s.Resource())
	}
	u, _ := url.Parse(req.ClientID)
	req.ClientHost, req.ClientName = u.Host, doc.ClientName
	return req, nil
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	req, err := s.parseAuthorize(r.Context(), r.URL.Query())
	if err != nil {
		page(w, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	page(w, http.StatusOK, pageData{Req: req})
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		page(w, http.StatusBadRequest, pageData{Error: "bad form"})
		return
	}
	req, err := s.parseAuthorize(r.Context(), r.PostForm)
	if err != nil {
		page(w, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	// One bucket for everyone: a stranger can block sign-in but never guess through.
	if !s.logins.Allow() {
		page(w, http.StatusTooManyRequests, pageData{Req: req, Error: "Too many attempts. Wait a few seconds and try again."})
		return
	}
	if !s.passphraseOK(r.PostForm.Get("passphrase")) {
		page(w, http.StatusUnauthorized, pageData{Req: req, Error: "Wrong passphrase."})
		return
	}

	code := s.signer.sign(claims{
		Kind: kindCode, ClientID: req.ClientID, RedirectURI: req.RedirectURI,
		Challenge: req.Challenge, ID: randomID(), Expires: s.now().Add(codeTTL).Unix(),
	})
	back, _ := url.Parse(req.RedirectURI) // validated above
	q := back.Query()
	q.Set("code", code)
	q.Set("iss", s.base)
	if req.State != "" {
		q.Set("state", req.State)
	}
	back.RawQuery = q.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request", "expected a form-encoded body")
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		c, err := s.signer.verify(f.Get("code"), kindCode, s.now())
		switch {
		case err != nil:
			tokenError(w, "invalid_grant", "invalid or expired code")
		case c.ClientID != f.Get("client_id") || c.RedirectURI != f.Get("redirect_uri"):
			tokenError(w, "invalid_grant", "code was issued to another client or redirect_uri")
		case !pkceOK(f.Get("code_verifier"), c.Challenge):
			tokenError(w, "invalid_grant", "code_verifier does not match")
		case !s.useCode(c.ID, time.Unix(c.Expires, 0)):
			tokenError(w, "invalid_grant", "code already used")
		default:
			s.issue(w, c.ClientID)
		}
	case "refresh_token":
		c, err := s.signer.verify(f.Get("refresh_token"), kindRefresh, s.now())
		switch {
		case err != nil:
			tokenError(w, "invalid_grant", "invalid or expired refresh token")
		case f.Get("client_id") != "" && f.Get("client_id") != c.ClientID:
			tokenError(w, "invalid_grant", "refresh token was issued to another client")
		default:
			s.issue(w, c.ClientID)
		}
	default:
		tokenError(w, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

// issue doesn't revoke earlier refresh tokens; rotating the signing key revokes everything.
func (s *Server) issue(w http.ResponseWriter, clientID string) {
	now := s.now()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": s.signer.sign(claims{
			Kind: kindAccess, ClientID: clientID, Audience: s.Resource(), Expires: now.Add(accessTTL).Unix(),
		}),
		"token_type": "Bearer",
		"expires_in": int(accessTTL.Seconds()),
		"refresh_token": s.signer.sign(claims{
			Kind: kindRefresh, ClientID: clientID, Expires: now.Add(refreshTTL).Unix(),
		}),
		"scope": scope,
	})
}

type clientMetadata struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
}

type cachedDoc struct {
	doc     clientMetadata
	fetched time.Time
}

func (s *Server) clientDoc(ctx context.Context, clientID string) (clientMetadata, error) {
	s.mu.Lock()
	cached, ok := s.docs[clientID]
	s.mu.Unlock()
	if ok && s.now().Sub(cached.fetched) < time.Hour {
		return cached.doc, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return clientMetadata{}, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return clientMetadata{}, fmt.Errorf("fetching client metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return clientMetadata{}, fmt.Errorf("fetching client metadata: %s", resp.Status)
	}
	var doc clientMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
		return clientMetadata{}, fmt.Errorf("decoding client metadata: %w", err)
	}
	if doc.ClientID != clientID {
		return clientMetadata{}, errors.New("client metadata names a different client_id")
	}
	s.mu.Lock()
	s.docs[clientID] = cachedDoc{doc: doc, fetched: s.now()}
	s.mu.Unlock()
	return doc, nil
}

// redirectAllowed matches exactly, except loopback URIs (Claude Code) match any port.
func redirectAllowed(uri string, registered []string) bool {
	if slices.Contains(registered, uri) {
		return true
	}
	u, err := url.Parse(uri)
	if err != nil || !isLoopback(u) || u.Scheme != "http" {
		return false
	}
	for _, reg := range registered {
		r, err := url.Parse(reg)
		if err == nil && isLoopback(r) && r.Scheme == u.Scheme &&
			r.Hostname() == u.Hostname() && r.Path == u.Path {
			return true
		}
	}
	return false
}

func isLoopback(u *url.URL) bool {
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func tokenError(w http.ResponseWriter, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": description})
}

type pageData struct {
	Req   authRequest
	Error string
}

func page(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY") // no clickjacking the passphrase form
	w.Header().Set("Content-Security-Policy", csp(d.Req.RedirectURI))
	w.WriteHeader(status)
	_ = signInPage.Execute(w, d)
}

// csp allows the redirect_uri origin in form-action, since browsers apply it to the post-submit redirect too.
func csp(redirectURI string) string {
	formAction := "'self'"
	if u, err := url.Parse(redirectURI); err == nil && u.Scheme != "" && u.Host != "" {
		formAction += " " + u.Scheme + "://" + u.Host
	}
	return "default-src 'none'; style-src 'unsafe-inline'; form-action " + formAction
}

var signInPage = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in to bootleg</title>
<style>
body{font:16px system-ui,sans-serif;background:#111;color:#eee;display:grid;place-items:center;min-height:100vh;margin:0}
main{max-width:22rem;padding:1.5rem}h1{font-size:1.3rem}input,button{font:inherit;width:100%;box-sizing:border-box;padding:.7rem;margin-top:.6rem;border-radius:.5rem;border:1px solid #444}
input{background:#1c1c1c;color:#eee}button{background:#d97757;color:#111;border:0;font-weight:600}.err{color:#f88}.muted{color:#999;font-size:.9rem}
</style></head><body><main>
<h1>bootleg</h1>
{{if .Req.ClientID}}
<p><strong>{{.Req.ClientHost}}</strong>{{if .Req.ClientName}} ({{.Req.ClientName}}){{end}} wants to add songs to your Apple Music.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="client_id" value="{{.Req.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.Req.RedirectURI}}">
<input type="hidden" name="state" value="{{.Req.State}}">
<input type="hidden" name="code_challenge" value="{{.Req.Challenge}}">
<input type="hidden" name="code_challenge_method" value="S256">
<input type="hidden" name="scope" value="{{.Req.Scope}}">
<input type="hidden" name="resource" value="{{.Req.Resource}}">
<label for="p">Passphrase</label>
<input id="p" type="password" name="passphrase" autocomplete="current-password" autofocus required>
<button type="submit">Sign in</button>
</form>
<p class="muted">Only the owner of this server has the passphrase.</p>
{{else}}
<p class="err">{{.Error}}</p>
{{end}}
</main></body></html>`))
