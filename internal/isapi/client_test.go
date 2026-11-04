package isapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at an httptest server.
func newTestClient(t *testing.T, server *httptest.Server, mutate func(*Config)) *Client {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	cfg := Config{
		IP:       parsed.Hostname(),
		Port:     port,
		Username: "admin",
		Password: "secret",
		Timeout:  2 * time.Second,
		Quiet:    true,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	client := New(cfg)
	t.Cleanup(client.Close)
	return client
}

func TestClientBaseURL(t *testing.T) {
	client := New(Config{IP: "192.168.1.10", Port: 8080})
	if got := client.BaseURL(); got != "http://192.168.1.10:8080" {
		t.Fatalf("BaseURL = %q", got)
	}
	secure := New(Config{IP: "192.168.1.10", Port: 443, UseSSL: true})
	if got := secure.BaseURL(); got != "https://192.168.1.10:443" {
		t.Fatalf("BaseURL = %q", got)
	}
	defaultPort := New(Config{IP: "192.168.1.10"})
	if got := defaultPort.BaseURL(); got != "http://192.168.1.10:80" {
		t.Fatalf("default BaseURL = %q", got)
	}
}

func TestClientGetThreeStateSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ISAPI/empty":
			w.WriteHeader(http.StatusOK)
		case "/ISAPI/text":
			_, _ = io.WriteString(w, "hello")
		case "/ISAPI/missing":
			http.Error(w, "nope", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	text, err := client.Get("/empty")
	if text != "" || err != nil {
		t.Fatalf("empty body = %q, %v; want \"\", nil", text, err)
	}
	text, err = client.Get("/text")
	if text != "hello" || err != nil {
		t.Fatalf("text body = %q, %v", text, err)
	}
	text, err = client.Get("/missing")
	if text != "" || err == nil {
		t.Fatalf("missing = %q, %v; want error", text, err)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
		t.Fatalf("missing error = %v, want 404 StatusError", err)
	}
}

func TestParseCachesEndpoint(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = io.WriteString(w, `<DeviceInfo><deviceName>NVR-1</deviceName></DeviceInfo>`)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	for i := 0; i < 3; i++ {
		node, err := client.Parse("/System/deviceInfo", "设备信息")
		if err != nil {
			t.Fatalf("Parse #%d: %v", i, err)
		}
		if node == nil || node.FindText("deviceName", "") != "NVR-1" {
			t.Fatalf("Parse #%d returned %#v", i, node)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestParseEmptyBodyIsCachedAsNoContent(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	for i := 0; i < 2; i++ {
		node, err := client.Parse("/System/deviceInfo", "设备信息")
		if node != nil || err != nil {
			t.Fatalf("Parse #%d = %#v, %v; want nil, nil", i, node, err)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestParseRejectsDTDAndCachesFailure(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><!DOCTYPE root [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><root>&xxe;</root>`)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	for i := 0; i < 2; i++ {
		node, err := client.Parse("/System/deviceInfo", "设备信息")
		if node != nil {
			t.Fatal("DTD document must not parse")
		}
		if !errors.Is(err, ErrDTD) {
			t.Fatalf("error = %v, want ErrDTD", err)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1 (deterministic failure cached)", got)
	}
}

// flakyTransport fails the first request and succeeds afterwards.
type flakyTransport struct {
	mu    sync.Mutex
	calls int
	body  string
}

func (f *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call == 1 {
		return nil, errors.New("temporary network failure")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    req,
	}, nil
}

func TestTransientNetworkFailureIsNotCached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	client := newTestClient(t, server, nil)
	transport := &flakyTransport{body: `<DeviceInfo><deviceName>NVR</deviceName></DeviceInfo>`}
	client.http.Transport = transport

	if _, err := client.Parse("/System/deviceInfo", "设备信息"); err == nil {
		t.Fatal("first parse should fail")
	} else {
		var transportErr *TransportError
		if !errors.As(err, &transportErr) {
			t.Fatalf("error = %v, want TransportError", err)
		}
	}
	node, err := client.Parse("/System/deviceInfo", "设备信息")
	if err != nil {
		t.Fatalf("second parse should retry and succeed: %v", err)
	}
	if node == nil || node.FindText("deviceName", "") != "NVR" {
		t.Fatalf("second parse node = %#v", node)
	}
	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want 2", transport.calls)
	}
}

func TestParseConcurrentSingleFlight(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, `<DeviceInfo><deviceName>NVR</deviceName></DeviceInfo>`)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	const goroutines = 20
	start := make(chan struct{})
	results := make([]*Node, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = client.Parse("/System/deviceInfo", "设备信息")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i] == nil || results[i].FindText("deviceName", "") != "NVR" {
			t.Fatalf("goroutine %d: bad result %#v", i, results[i])
		}
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestClientPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/xml" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "<request/>" {
			t.Errorf("body = %q", body)
		}
		_, _ = io.WriteString(w, `<response>ok</response>`)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	text, err := client.Post("/ContentMgmt/search", "<request/>")
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if text != `<response>ok</response>` {
		t.Fatalf("response = %q", text)
	}
}

func TestClientTLSInsecureSkipVerify(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<DeviceInfo><deviceName>NVR</deviceName></DeviceInfo>`)
	}))
	defer server.Close()
	client := newTestClient(t, server, func(cfg *Config) { cfg.UseSSL = true })

	node, err := client.Parse("/System/deviceInfo", "设备信息")
	if err != nil {
		t.Fatalf("Parse over TLS: %v", err)
	}
	if node == nil || node.FindText("deviceName", "") != "NVR" {
		t.Fatalf("node = %#v", node)
	}
}

func TestSetContextCancelsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<root/>")
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.SetContext(ctx)
	if err := client.CheckCancel(); err == nil {
		t.Fatal("CheckCancel should fail")
	}
	if _, err := client.Parse("/System/deviceInfo", "设备信息"); err == nil {
		t.Fatal("Parse should fail on cancelled context")
	}
}

func ExampleClient_BaseURL() {
	client := New(Config{IP: "10.0.0.1", Port: 80})
	fmt.Println(client.BaseURL())
	// Output: http://10.0.0.1:80
}

func TestGetQuietSuppressesProbeWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	var logs []string
	client := newTestClient(t, server, func(c *Config) {
		c.Quiet = false
		c.Logger = func(msg string) { logs = append(logs, msg) }
	})

	if _, err := client.Get("/probe"); err == nil {
		t.Fatal("Get should return an error for 403")
	}
	if len(logs) == 0 {
		t.Fatal("Get should log a warning for 403")
	}
	logs = nil
	if _, err := client.GetQuiet("/probe"); err == nil {
		t.Fatal("GetQuiet should still return an error")
	}
	if len(logs) != 0 {
		t.Fatalf("GetQuiet logged %v, want silence", logs)
	}
}
