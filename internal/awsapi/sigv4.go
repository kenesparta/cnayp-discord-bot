// Package awsapi signs requests to AWS and speaks the one AWS protocol the bot
// needs, DynamoDB's JSON API.
//
// It is written on the standard library for the same reason the Discord client
// is: the module takes no third-party dependency (CLAUDE.md), and Signature
// Version 4 is a page of hashing. It knows nothing about Discord or about the
// announcer's records.
package awsapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Credentials are the AWS credentials a request is signed with: on Lambda, the
// execution role's, which internal/config reads from the environment. Every way
// of printing them is redacted, so they cannot reach a log line by accident.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is set for temporary credentials, which is what an execution
	// role always has.
	SessionToken string
}

// String keeps the credentials out of fmt's %v and %s.
func (Credentials) String() string { return "awsapi.Credentials{redacted}" }

// GoString keeps the credentials out of fmt's %#v.
func (c Credentials) GoString() string { return c.String() }

// LogValue keeps the credentials out of slog.
func (c Credentials) LogValue() slog.Value { return slog.StringValue(c.String()) }

const algorithm = "AWS4-HMAC-SHA256"

// Sign adds the Signature Version 4 headers to req: X-Amz-Date,
// X-Amz-Security-Token when the credentials carry one, and Authorization.
//
// body must be exactly the bytes req will send. Every header already on req is
// signed, together with Host; a header the transport adds later (User-Agent,
// Accept-Encoding) is not, which SigV4 allows.
//
// The canonical path is req.URL's escaped path, encoded once. That is correct
// for the paths this package signs, which carry no character that needs
// escaping; a path that did would need the double encoding AWS applies to
// every service but S3.
func Sign(req *http.Request, body []byte, creds Credentials, region, service string, now time.Time) {
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed, headers := canonicalHeaders(req.Header, host)
	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL),
		headers,
		signed,
		sha256Hex(body),
	}, "\n")

	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := strings.Join([]string{algorithm, stamp, scope, sha256Hex([]byte(canonical))}, "\n")

	key := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", algorithm+" Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+signature)
}

// canonicalHeaders returns the signed header names and the canonical header
// block: lower-case names in order, each with its values trimmed, inner runs
// of spaces collapsed, and repeated values joined by commas.
func canonicalHeaders(header http.Header, host string) (string, string) {
	values := map[string]string{"host": host}
	for name, list := range header {
		lower := strings.ToLower(name)
		if lower == "authorization" {
			continue
		}
		cleaned := make([]string, len(list))
		for i, v := range list {
			cleaned[i] = strings.Join(strings.Fields(v), " ")
		}
		values[lower] = strings.Join(cleaned, ",")
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var block strings.Builder
	for _, name := range names {
		block.WriteString(name + ":" + values[name] + "\n")
	}
	return strings.Join(names, ";"), block.String()
}

func canonicalPath(u *url.URL) string {
	if path := u.EscapedPath(); path != "" {
		return path
	}
	return "/"
}

// canonicalQuery sorts the parameters by name, then by value, each encoded as
// RFC 3986 asks: a space is %20, never +.
func canonicalQuery(u *url.URL) string {
	query := u.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var pairs []string
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		for _, value := range values {
			pairs = append(pairs, uriEncode(key)+"="+uriEncode(value))
		}
	}
	return strings.Join(pairs, "&")
}

// uriEncode percent-encodes every byte but the RFC 3986 unreserved set.
func uriEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var out strings.Builder
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hexDigits[c>>4])
		out.WriteByte(hexDigits[c&0x0f])
	}
	return out.String()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
