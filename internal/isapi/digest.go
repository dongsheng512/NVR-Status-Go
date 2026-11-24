package isapi

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// digestChallenge is the subset of a WWW-Authenticate: Digest challenge this
// client understands (RFC 7616/2617 with MD5).
type digestChallenge struct {
	Realm     string
	Nonce     string
	Opaque    string
	Algorithm string
	Qop       string
	Stale     bool
}

// findDigestChallenge returns the best supported Digest challenge carried by
// resp (falling back to the first parsed Digest challenge so unsupported
// algorithm errors are still reported accurately).
func findDigestChallenge(resp *http.Response) (*digestChallenge, bool) {
	if resp == nil {
		return nil, false
	}
	var fallback *digestChallenge
	for _, header := range resp.Header.Values("WWW-Authenticate") {
		if challenge, ok := parseDigestChallenge(header); ok {
			if digestAlgorithmSupported(challenge.Algorithm) {
				return challenge, true
			}
			if fallback == nil {
				fallback = challenge
			}
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}

func digestAlgorithmSupported(alg string) bool {
	switch strings.ToUpper(strings.TrimSpace(alg)) {
	case "", "MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS":
		return true
	default:
		return false
	}
}

// parseDigestChallenge parses a single WWW-Authenticate header value. It
// tolerates Basic challenges, comma-combined headers, and unknown extensions.
func parseDigestChallenge(header string) (*digestChallenge, bool) {
	text := strings.TrimSpace(header)
	rest := ""
	if len(text) >= len("Digest") && strings.EqualFold(text[:len("Digest")], "Digest") {
		r := text[len("Digest"):]
		if r == "" || r[0] == ' ' || r[0] == '\t' {
			rest = r
		}
	} else {
		// Handle combined headers such as `Basic realm="x", Digest realm="y", nonce="..."`.
		lower := strings.ToLower(text)
		if idx := strings.Index(lower, "digest "); idx >= 0 {
			rest = text[idx+len("digest"):]
		}
	}
	if rest == "" {
		return nil, false
	}
	params := parseAuthParams(rest)
	nonce := params["nonce"]
	if nonce == "" {
		return nil, false
	}
	return &digestChallenge{
		Realm:     params["realm"],
		Nonce:     nonce,
		Opaque:    params["opaque"],
		Algorithm: params["algorithm"],
		Qop:       params["qop"],
		Stale:     strings.EqualFold(params["stale"], "true"),
	}, true
}

// parseAuthParams parses comma-separated key="value" / key=token pairs. Quoted
// values may contain commas and escaped quotes.
func parseAuthParams(s string) map[string]string {
	params := make(map[string]string)
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		key := strings.ToLower(strings.TrimSpace(s[start:i]))
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if key == "" || i >= len(s) || s[i] != '=' {
			// A bare token without a value; skip through the next comma.
			for i < len(s) && s[i] != ',' {
				i++
			}
			continue
		}
		i++ // '='
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] == '"' {
			i++
			var value strings.Builder
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					value.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				value.WriteByte(s[i])
				i++
			}
			params[key] = value.String()
			continue
		}
		start = i
		for i < len(s) && s[i] != ',' {
			i++
		}
		params[key] = strings.TrimSpace(s[start:i])
	}
	return params
}

// authorization builds the Authorization response for one request (nc=1).
func (ch *digestChallenge) authorization(method, uri, username, password string) (string, error) {
	return ch.authorizationWithNC(method, uri, username, password, 1)
}

func (ch *digestChallenge) authorizationWithNC(method, uri, username, password string, ncVal uint32) (string, error) {
	algorithm := strings.ToUpper(strings.TrimSpace(ch.Algorithm))
	if algorithm == "" {
		algorithm = "MD5"
	}
	var hashFn func(string) string
	switch algorithm {
	case "MD5", "MD5-SESS":
		hashFn = md5Hex
	case "SHA-256", "SHA-256-SESS":
		hashFn = sha256Hex
	default:
		return "", fmt.Errorf("unsupported digest algorithm %q", ch.Algorithm)
	}
	qop, err := selectQop(ch.Qop)
	if err != nil {
		return "", err
	}

	cnonceBytes := make([]byte, 8)
	if _, err := rand.Read(cnonceBytes); err != nil {
		return "", fmt.Errorf("generate digest cnonce: %w", err)
	}
	cnonce := hex.EncodeToString(cnonceBytes)

	ha1 := hashFn(username + ":" + ch.Realm + ":" + password)
	if strings.HasSuffix(algorithm, "-SESS") {
		ha1 = hashFn(ha1 + ":" + ch.Nonce + ":" + cnonce)
	}
	ha2 := hashFn(method + ":" + uri)

	if ncVal == 0 {
		ncVal = 1
	}
	nc := fmt.Sprintf("%08x", ncVal)
	var response string
	if qop != "" {
		response = hashFn(ha1 + ":" + ch.Nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = hashFn(ha1 + ":" + ch.Nonce + ":" + ha2)
	}

	parts := []string{
		"username=" + quoteDigest(username),
		"realm=" + quoteDigest(ch.Realm),
		"nonce=" + quoteDigest(ch.Nonce),
		"uri=" + quoteDigest(uri),
		"response=" + quoteDigest(response),
		"algorithm=" + algorithm,
	}
	if qop != "" {
		parts = append(parts,
			"qop="+qop,
			"nc="+nc,
			"cnonce="+quoteDigest(cnonce),
		)
	}
	if ch.Opaque != "" {
		parts = append(parts, "opaque="+quoteDigest(ch.Opaque))
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

func selectQop(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// RFC 2069 fallback: response without qop/nc/cnonce.
		return "", nil
	}
	for _, item := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "auth":
			return "auth", nil
		}
	}
	return "", fmt.Errorf("unsupported digest qop %q", raw)
}

func md5Hex(text string) string {
	sum := md5.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func quoteDigest(text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, `"`, `\"`)
	return `"` + text + `"`
}
