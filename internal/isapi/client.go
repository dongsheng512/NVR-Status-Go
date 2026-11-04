// Package isapi implements the Hikvision ISAPI HTTP client: Digest
// authentication, safe XML parsing, endpoint caching and the device-level
// information endpoints.
package isapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
)

const (
	defaultTimeout = 10 * time.Second
	userAgent      = "hikvision_status/1.0"
)

// Config configures a Client. Zero values are usable: Port defaults to 80 and
// Timeout to 10s.
type Config struct {
	IP       string
	Username string
	Password string
	Port     int
	UseSSL   bool
	Timeout  time.Duration // default 10s
	Quiet    bool
	Logger   func(string) // optional; receives warnings/progress text
	Progress func(model.Progress)
}

// StatusError reports a deterministic HTTP failure (non-200 response).
type StatusError struct {
	Endpoint   string
	StatusCode int
	Status     string
}

func (e *StatusError) Error() string {
	if e.Status != "" {
		return fmt.Sprintf("%s: HTTP %d %s", e.Endpoint, e.StatusCode, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d", e.Endpoint, e.StatusCode)
}

// TransportError reports a transient network failure. Such failures are never
// negatively cached: a later call may retry the endpoint.
type TransportError struct {
	Endpoint string
	Err      error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("%s: %v", e.Endpoint, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

type parseEntry struct {
	done chan struct{}
	node *Node
	err  error
}

// Client is safe for concurrent use. Its HTTP client and TLS configuration are
// shared, while digest authentication state is per request.
type Client struct {
	cfg     Config
	http    *http.Client
	baseURL string

	ctxMu sync.RWMutex
	ctx   context.Context

	cacheMu sync.Mutex
	cache   map[string]*parseEntry

	tzMu sync.Mutex
	tz   *time.Location

	camerasMu     sync.Mutex
	camerasLoaded bool
	cameras       []model.Camera

	sslNotice sync.Once
}

// New creates a client and a shared HTTP transport. For HTTPS, certificate
// verification is skipped like the Python client's verify=False (NVRs usually
// present self-signed certificates on trusted LANs).
func New(cfg Config) *Client {
	if cfg.Port <= 0 {
		cfg.Port = 80
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   cfg.Timeout,
		ExpectContinueTimeout: time.Second,
	}
	if cfg.UseSSL {
		//nolint:gosec // internal tool: NVRs use self-signed certificates.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	return &Client{
		cfg:     cfg,
		http:    &http.Client{Transport: transport, Timeout: cfg.Timeout},
		baseURL: fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(cfg.IP, strconv.Itoa(cfg.Port))),
		ctx:     context.Background(),
		cache:   make(map[string]*parseEntry),
	}
}

// Close releases idle HTTP connections. The client must not be reused after
// Close in the current scan; a new Client is created per device.
func (c *Client) Close() {
	if c == nil || c.http == nil {
		return
	}
	c.http.CloseIdleConnections()
}

// SetContext installs the context used by every subsequent request and cache
// wait. Cancelling it makes in-flight and future calls return
// util.ErrCancelled.
func (c *Client) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.ctxMu.Lock()
	c.ctx = ctx
	c.ctxMu.Unlock()
}

func (c *Client) currentContext() context.Context {
	c.ctxMu.RLock()
	ctx := c.ctx
	c.ctxMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// CheckCancel reports whether the configured context has been cancelled.
func (c *Client) CheckCancel() error {
	return util.CheckCancel(c.currentContext())
}

// BaseURL returns the scheme://host:port prefix (without /ISAPI).
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Get performs GET /ISAPI<endpoint>. It returns ("", nil) for an empty 200
// body, the response text for a non-empty body, and an error for network
// failures or non-200 responses.
func (c *Client) Get(endpoint string) (string, error) {
	return c.fetchText(http.MethodGet, endpoint, "", endpoint, false)
}

// GetQuiet is Get without the warning log, for capability probes that are
// expected to 403/404 on many firmwares (e.g. loop-overwrite endpoints).
func (c *Client) GetQuiet(endpoint string) (string, error) {
	return c.fetchText(http.MethodGet, endpoint, "", endpoint, true)
}

// Post performs POST /ISAPI<endpoint> with an XML body. It returns the response
// text, or an error for network failures or non-200 responses.
func (c *Client) Post(endpoint, body string) (string, error) {
	return c.fetchText(http.MethodPost, endpoint, body, endpoint, false)
}

// Parse fetches and parses endpoint, caching both successes and deterministic
// failures. Concurrent calls for the same endpoint share one request
// (single-flight). A nil Node with a nil error means "no content" (empty body).
func (c *Client) Parse(endpoint, tag string) (*Node, error) {
	return c.parse(endpoint, tag, false)
}

// parse is Parse with an optional quiet mode used by capability probes.
func (c *Client) parse(endpoint, tag string, quiet bool) (*Node, error) {
	if err := c.CheckCancel(); err != nil {
		return nil, err
	}
	ctx := c.currentContext()

	c.cacheMu.Lock()
	if entry, ok := c.cache[endpoint]; ok {
		c.cacheMu.Unlock()
		select {
		case <-entry.done:
			return entry.node, entry.err
		case <-ctx.Done():
			return nil, util.ErrCancelled
		}
	}
	entry := &parseEntry{done: make(chan struct{})}
	c.cache[endpoint] = entry
	c.cacheMu.Unlock()

	node, err := c.loadParse(endpoint, tag, quiet)

	c.cacheMu.Lock()
	entry.node, entry.err = node, err
	if isTransient(err) {
		// Network failures must not be negatively cached; allow a later retry.
		delete(c.cache, endpoint)
	}
	close(entry.done)
	c.cacheMu.Unlock()
	return node, err
}

func (c *Client) loadParse(endpoint, tag string, quiet bool) (*Node, error) {
	text, err := c.fetchText(http.MethodGet, endpoint, "", tag, quiet)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	label := tag
	if label == "" {
		label = endpoint
	}
	node, err := ParseXML([]byte(text), label)
	if err != nil {
		if !quiet && (errors.Is(err, ErrDTD) || errors.Is(err, ErrXMLTooLarge)) {
			c.logMessage("⚠️ " + err.Error())
		}
		return nil, err
	}
	return node, nil
}

func (c *Client) fetchText(method, endpoint, body, tag string, quiet bool) (string, error) {
	if err := c.CheckCancel(); err != nil {
		return "", err
	}
	resp, err := c.doRequest(method, endpoint, body)
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return "", err
		}
		if !quiet {
			c.warnFail(tag, err.Error())
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		statusErr := &StatusError{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
		}
		if !quiet {
			c.warnFail(tag, fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		return "", statusErr
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, XMLMaxBytes+1))
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return "", err
		}
		if !quiet {
			c.warnFail(tag, err.Error())
		}
		return "", &TransportError{Endpoint: endpoint, Err: err}
	}
	if len(data) > XMLMaxBytes {
		tooLarge := fmt.Errorf("%w: %s", ErrXMLTooLarge, tag)
		if !quiet {
			c.warnFail(tag, "响应过大")
		}
		return "", tooLarge
	}
	return string(data), nil
}

func (c *Client) doRequest(method, endpoint, body string) (*http.Response, error) {
	if c.cfg.UseSSL {
		c.sslNotice.Do(func() {
			c.logMessage("⚠️ HTTPS 模式已跳过证书校验(自签证书),仅限可信内网使用")
		})
	}
	ctx := c.currentContext()
	rawURL := c.baseURL + "/ISAPI" + endpoint

	resp, err := c.send(ctx, method, rawURL, body, "")
	if err != nil {
		return nil, wrapTransportError(endpoint, err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge, ok := findDigestChallenge(resp)
	if !ok {
		return resp, nil
	}

	// Consume the challenge body so the connection can be reused, then retry
	// exactly once with the computed Authorization header.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()

	uri := endpoint
	if parsed, err := url.Parse(rawURL); err == nil {
		uri = parsed.RequestURI()
	}
	authorization, err := challenge.authorization(method, uri, c.cfg.Username, c.cfg.Password)
	if err != nil {
		return nil, err
	}
	resp, err = c.send(ctx, method, rawURL, body, authorization)
	if err != nil {
		return nil, wrapTransportError(endpoint, err)
	}
	return resp, nil
}

func (c *Client) send(ctx context.Context, method, rawURL, body, authorization string) (*http.Response, error) {
	var reader io.Reader
	if method == http.MethodPost || body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/xml")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, util.ErrCancelled
		}
		return nil, err
	}
	return resp, nil
}

func wrapTransportError(endpoint string, err error) error {
	if errors.Is(err, util.ErrCancelled) {
		return util.ErrCancelled
	}
	return &TransportError{Endpoint: endpoint, Err: err}
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return true
	}
	return errors.Is(err, util.ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (c *Client) warnFail(tag, reason string) {
	if strings.TrimSpace(tag) == "" {
		tag = "请求"
	}
	c.logMessage(fmt.Sprintf("⚠️ %s获取失败: %s", tag, reason))
}

func (c *Client) logMessage(message string) {
	if c.cfg.Logger != nil {
		// Match the Python client: a broken GUI callback must never abort the
		// scan or leave a cache entry permanently in flight.
		func() {
			defer func() { _ = recover() }()
			c.cfg.Logger(message)
		}()
		return
	}
	if !c.cfg.Quiet {
		fmt.Println(message)
	}
}

func (c *Client) reportProgress(progress model.Progress) {
	if c.cfg.Progress == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		c.cfg.Progress(progress)
	}()
}
