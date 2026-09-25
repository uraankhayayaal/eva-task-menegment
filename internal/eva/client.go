package eva

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"evasimilar/internal/config"
)

// Client talks to the Eva API (JSON-RPC 2.2) documented in the OpenAPI spec
// oas_evateam_v1_9_22.json:
//
//	POST {host}/api/?m=CmfTask.list
//	{"jsonrpc":"2.2","method":"CmfTask.list","callid":"<uuid>","kwargs":{...}}
//
// kwargs carries filter (array of [field, op, value] triples), fields, slice
// (the half-open range [start, end)), order_by and include_archived.
type Client struct {
	rpcURL     string
	authHeader string
	login      string
	password   string
	authURL    string // SSO form-login URL (e.g. {host}/auth/signin)
	http       *http.Client
}

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	CallID  string         `json:"callid"`
	Args    []any          `json:"args,omitempty"`
	Kwargs  map[string]any `json:"kwargs"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"callid,omitempty"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("eva rpc error %d: %s", e.Code, e.Message)
}

func New(rpcURL, authHeader, login, password, authURL string) *Client {
	return &Client{
		rpcURL:     rpcURL,
		authHeader: authHeader,
		login:      login,
		password:   password,
		authURL:    authURL,
		http:       &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// NewWithAPIToken builds a client that authenticates every request with the
// configured header (e.g. "Authorization: Bearer <token>") — the priority
// auth scheme.
func NewWithAPIToken(rpcURL, apiToken, header, scheme string) *Client {
	h := header
	if header == "" {
		h = "Authorization"
	}
	if scheme == "" {
		scheme = "Bearer"
	}
	v := apiToken
	if scheme != "" && !strings.HasPrefix(strings.ToLower(scheme), "none") {
		v = scheme + " " + v
	}
	c := New(rpcURL, "", "", "", "")
	c.authHeader = h + ": " + v
	return c
}

// NewFromConfig picks the auth scheme by priority:
// API token > custom auth header > login/password (SSO) > none.
func NewFromConfig(cfg config.Config) *Client {
	if cfg.EvaAPIToken != "" {
		return NewWithAPIToken(cfg.EvaRPCURL, cfg.EvaAPIToken, cfg.EvaAPITokenHeader, cfg.EvaAPITokenScheme)
	}
	return New(cfg.EvaRPCURL, cfg.EvaAuthHeader, cfg.EvaLogin, cfg.EvaPassword, cfg.EvaAuthLoginURL)
}

// AuthMode reports which auth scheme is active (safe to log).
func AuthMode(cfg config.Config) string {
	switch {
	case cfg.EvaAPIToken != "":
		return "api_token"
	case cfg.EvaAuthHeader != "":
		return "custom_header"
	case cfg.EvaLogin != "":
		return "login_password"
	default:
		return "none"
	}
}

// Call dispatches a JSON-RPC 2.2 request for "Model.method" with the given
// kwargs and decodes the "result" field into v (v must be a pointer).
func (c *Client) Call(ctx context.Context, method string, kwargs map[string]any) (json.RawMessage, error) {
	return c.CallRaw(ctx, method, nil, kwargs)
}

// CallRaw dispatches a JSON-RPC 2.2 request for "Model.method" with optional
// positional args and kwargs (both are put top-level in the body). Some Eva
// endpoints address their target via args[0] (e.g. CmfComment.update/delete).
func (c *Client) CallRaw(ctx context.Context, method string, args []any, kwargs map[string]any) (json.RawMessage, error) {
	url := c.endpoint(method)
	reqBody := rpcRequest{
		JSONRPC: "2.2",
		Method:  method,
		CallID:  randomUUID(),
		Args:    args,
		Kwargs:  kwargs,
	}
	if reqBody.Kwargs == nil {
		reqBody.Kwargs = map[string]any{}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authHeader != "" {
		parts := strings.SplitN(c.authHeader, ":", 2)
		if len(parts) == 2 {
			req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		} else {
			req.Header.Set("Authorization", c.authHeader)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eva rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if isRedirect(resp.StatusCode) {
		return nil, fmt.Errorf("eva rpc %s: http %d -> unauthenticated or wrong endpoint (redirect to login). Check EVA_API_TOKEN / EVA_RPC_URL", method, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("eva rpc %s: http %d: %s", method, resp.StatusCode, truncate(string(data), 500))
	}
	var rpc rpcResponse
	if err := json.Unmarshal(data, &rpc); err != nil {
		detail := truncate(string(data), 300)
		hint := ""
		if looksLikeHTML(data) {
			hint = " (endpoint returned an HTML page, not JSON — check EVA_RPC_URL and auth)"
		}
		return nil, fmt.Errorf("eva rpc %s @ %s: decode response: %w; body: %s%s", method, url, err, detail, hint)
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

// endpoint builds "POST {base}/?m=<method>".
func (c *Client) endpoint(method string) string {
	return strings.TrimRight(c.rpcURL, "/") + "/?m=" + method
}

// Auth logs in against the SSO form endpoint (EVA_AUTH_LOGIN_URL) with
// EVA_LOGIN / EVA_PASSWORD and persists the session cookie. Skipped when a
// token/auth header is configured — token auth takes priority.
func (c *Client) Auth(ctx context.Context) error {
	if c.authHeader != "" {
		return nil
	}
	if c.login == "" || c.password == "" {
		return nil
	}
	if c.authURL == "" {
		return fmt.Errorf("EVA_AUTH_LOGIN_URL is required when EVA_LOGIN is set")
	}
	form := url.Values{}
	form.Set("login", c.login)
	form.Set("password", c.password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("eva auth: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if c.http.Jar == nil {
		ck := &cookieJar{cookies: map[string][]*http.Cookie{}}
		for _, c := range resp.Cookies() {
			ck.cookies[c.Domain] = append(ck.cookies[c.Domain], c)
		}
		c.http.Jar = ck
	}
	if !hasSessionCookie(c.http.Jar, req.URL) && resp.StatusCode >= 400 {
		return fmt.Errorf("eva auth: http %d (invalid credentials?)", resp.StatusCode)
	}
	return nil
}

type cookieJar struct {
	cookies map[string][]*http.Cookie
}

func (c *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	c.cookies[u.Host] = cookies
}

func (c *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	return c.cookies[u.Host]
}

func hasSessionCookie(jar http.CookieJar, u *url.URL) bool {
	if jar == nil {
		return false
	}
	for _, ck := range jar.Cookies(u) {
		name := strings.ToLower(ck.Name)
		if strings.Contains(name, "session") || strings.Contains(name, "token") || strings.Contains(name, "sso") || strings.Contains(name, "auth") {
			return true
		}
	}
	return false
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func isRedirect(code int) bool {
	return code == http.StatusFound || code == http.StatusMovedPermanently || code == http.StatusTemporaryRedirect || code == http.StatusPermanentRedirect
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func looksLikeHTML(b []byte) bool {
	head := bytes.TrimSpace(b)
	if len(head) == 0 {
		return false
	}
	if head[0] == '<' {
		for _, tag := range [][]byte{[]byte("<!doctype"), []byte("<html"), []byte("<title"), []byte("<body"), []byte("<head")} {
			if len(head) >= len(tag) && bytes.EqualFold(head[:len(tag)], tag) {
				return true
			}
		}
	}
	return false
}
