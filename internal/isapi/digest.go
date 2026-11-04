package isapi

import (
	"crypto/md5"
	"crypto/rand"
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

// findDigestChallenge returns the first Digest challenge carried by resp.
func findDigestChallenge(resp *http.Response) (*digestChallenge, bool) {
	if resp == nil {
		return nil, false
	}
	for _, header := range resp.Header.Values("WWW-Authenticate") {
		if challenge, ok := parseDigestChallenge(header); ok {
			return challenge, true
		}
	}
	return nil, false
}

// parseDigestChallenge parses a single WWW-Authenticate header value. It
// tolerates Basic challenges and unknown auth-param extensions.
func parseDigestChallenge(header string) (*digestChallenge, bool) {
	text := strings.TrimSpace(header)
	if len(text) < len("Digest") || !strings.EqualFold(text[:len("Digest")], "Digest") {
		return nil, false
	}
	rest := text[len("Digest"):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
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

// authorization builds the Authorization response for one request.
func (ch *digestChallenge) authorization(method, uri, username, password string) (string, error) {
	algorithm := strings.ToUpper(strings.TrimSpace(ch.Algorithm))
	if algorithm == "" {
		algorithm = "MD5"
	}
	if algorithm != "MD5" && algorithm != "MD5-SESS" {
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

	ha1 := md5Hex(username + ":" + ch.Realm + ":" + password)
	if algorithm == "MD5-SESS" {
		ha1 = md5Hex(ha1 + ":" + ch.Nonce + ":" + cnonce)
	}
	ha2 := md5Hex(method + ":" + uri)

	const nc = "00000001"
	var response string
	if qop != "" {
		response = md5Hex(ha1 + ":" + ch.Nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5Hex(ha1 + ":" + ch.Nonce + ":" + ha2)
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

func quoteDigest(text string) string {
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, `"`, `\"`)
	return `"` + text + `"`
}
