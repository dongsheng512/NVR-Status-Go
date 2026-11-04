package isapi

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testDigestRealm  = "test-realm"
	testDigestNonce  = "nonce-abc-123"
	testDigestOpaque = "opaque-123"
)

func TestParseDigestChallenge(t *testing.T) {
	tests := []struct {
		name   string
		header string
		wantOK bool
		realm  string
		nonce  string
		opaque string
		qop    string
		algo   string
	}{
		{
			name:   "standard",
			header: `Digest realm="IP Camera", qop="auth", nonce="abc", opaque="xyz", algorithm=MD5`,
			wantOK: true, realm: "IP Camera", nonce: "abc", opaque: "xyz", qop: "auth", algo: "MD5",
		},
		{
			name:   "lowercase and quoted qop list",
			header: `digest realm="r", nonce="n", qop="auth,auth-int", algorithm="MD5-sess"`,
			wantOK: true, realm: "r", nonce: "n", qop: "auth,auth-int", algo: "MD5-sess",
		},
		{
			name:   "realm with escaped quote",
			header: `Digest realm="a\"b", nonce="n"`,
			wantOK: true, realm: `a"b`, nonce: "n",
		},
		{
			name:   "basic ignored",
			header: `Basic realm="IP Camera"`,
			wantOK: false,
		},
		{
			name:   "missing nonce",
			header: `Digest realm="r", qop="auth"`,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			challenge, ok := parseDigestChallenge(tt.header)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if challenge.Realm != tt.realm || challenge.Nonce != tt.nonce || challenge.Opaque != tt.opaque || challenge.Qop != tt.qop || challenge.Algorithm != tt.algo {
				t.Fatalf("challenge = %+v", challenge)
			}
		})
	}
}

func TestDigestAuthorizationRFC2617Vector(t *testing.T) {
	// RFC 2617 §3.5 example, except cnonce is generated per request. Compute
	// the expected response from the cnonce actually placed in the header.
	const realm = "example-realm"
	challenge := &digestChallenge{Realm: realm, Nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093", Qop: "auth"}
	header, err := challenge.authorization("GET", "/dir/index.html", "Mufasa", "Circle Of Life")
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	params := parseAuthParams(strings.TrimPrefix(header, "Digest "))
	cnonce := params["cnonce"]
	if cnonce == "" {
		t.Fatal("missing cnonce")
	}
	ha1 := testMD5("Mufasa:" + realm + ":Circle Of Life")
	ha2 := testMD5("GET:/dir/index.html")
	want := testMD5(ha1 + ":" + challenge.Nonce + ":00000001:" + cnonce + ":auth:" + ha2)
	if got := params["response"]; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	if params["nc"] != "00000001" || params["qop"] != "auth" {
		t.Fatalf("qop/nc = %q/%q", params["qop"], params["nc"])
	}
}

// newDigestServer starts a server that issues a Digest challenge and validates
// the retried Authorization header. It returns the server and a counter of
// HTTP requests received.
func newDigestServer(t *testing.T, username, password, body string) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Digest ") {
			challenge := fmt.Sprintf(`Digest realm=%q, qop="auth", nonce=%q, opaque=%q, algorithm=MD5`,
				testDigestRealm, testDigestNonce, testDigestOpaque)
			w.Header().Set("WWW-Authenticate", challenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := validateDigestRequest(r, auth, username, password); err != nil {
			t.Errorf("invalid Authorization header: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func validateDigestRequest(r *http.Request, header, username, password string) error {
	params := parseAuthorizationHeader(header)
	for key, want := range map[string]string{
		"username": username,
		"realm":    testDigestRealm,
		"nonce":    testDigestNonce,
		"opaque":   testDigestOpaque,
		"uri":      r.URL.RequestURI(),
		"qop":      "auth",
		"nc":       "00000001",
	} {
		if got := params[key]; got != want {
			return fmt.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	cnonce := params["cnonce"]
	if cnonce == "" {
		return errors.New("missing cnonce")
	}
	ha1 := testMD5(username + ":" + testDigestRealm + ":" + password)
	ha2 := testMD5(r.Method + ":" + r.URL.RequestURI())
	want := testMD5(ha1 + ":" + testDigestNonce + ":" + params["nc"] + ":" + cnonce + ":auth:" + ha2)
	if got := params["response"]; got != want {
		return fmt.Errorf("response = %q, want %q", got, want)
	}
	return nil
}

var (
	quotedAuthParam = regexp.MustCompile(`(?:^|[,\s])([A-Za-z][A-Za-z0-9_-]*)="((?:[^"\\]|\\.)*)"`)
	tokenAuthParam  = regexp.MustCompile(`(?:^|[,\s])([A-Za-z][A-Za-z0-9_-]*)=([^,\s]+)`)
)

func parseAuthorizationHeader(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimSpace(strings.TrimPrefix(header, "Digest "))
	for _, match := range quotedAuthParam.FindAllStringSubmatch(header, -1) {
		params[strings.ToLower(match[1])] = strings.ReplaceAll(match[2], `\"`, `"`)
	}
	for _, match := range tokenAuthParam.FindAllStringSubmatch(header, -1) {
		key := strings.ToLower(match[1])
		if _, exists := params[key]; !exists {
			params[key] = strings.Trim(match[2], `"`)
		}
	}
	return params
}

func testMD5(text string) string {
	sum := md5.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestDigestChallengeSuccess(t *testing.T) {
	const body = `<DeviceInfo><deviceName>NVR</deviceName></DeviceInfo>`
	server, requests := newDigestServer(t, "admin", "secret", body)
	client := newTestClient(t, server, nil)

	node, err := client.Parse("/System/deviceInfo", "设备信息")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if node == nil || node.FindText("deviceName", "") != "NVR" {
		t.Fatalf("unexpected node: %#v", node)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("requests = %d, want 2 (challenge + authenticated retry)", got)
	}

	// Cached: a second parse must not touch the network again.
	if _, err := client.Parse("/System/deviceInfo", "设备信息"); err != nil {
		t.Fatalf("cached Parse: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Fatalf("requests after cached parse = %d, want 2", got)
	}
}

func TestDigestChallengeFailureRetriesExactlyOnce(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Digest realm=%q, qop="auth", nonce=%q, opaque=%q, algorithm=MD5`,
			testDigestRealm, testDigestNonce, testDigestOpaque))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(t, server, nil)
	node, err := client.Parse("/System/deviceInfo", "设备信息")
	if node != nil {
		t.Fatal("expected nil node")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want 401 StatusError", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("requests = %d, want exactly 2", got)
	}

	// A 401 after the one retry is a deterministic failure and must be cached.
	if _, err := client.Parse("/System/deviceInfo", "设备信息"); err == nil {
		t.Fatal("expected cached error")
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("requests after cached error = %d, want 2", got)
	}
}
