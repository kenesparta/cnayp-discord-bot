package awsapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The example credentials and clock of AWS's published Signature Version 4
// test suite.
var (
	suiteCredentials = Credentials{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	suiteClock = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
)

func TestSignMatchesTheGetVanillaVector(t *testing.T) {
	// get-vanilla from the AWS SigV4 test suite: GET / with no query and no
	// body, signed for the service "service" in us-east-1.
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	Sign(req, nil, suiteCredentials, "us-east-1", "service", suiteClock)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n %s\nwant\n %s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
}

func TestSignAddsTheSessionTokenAndSignsIt(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://dynamodb.us-east-1.amazonaws.com/", nil)
	creds := suiteCredentials
	creds.SessionToken = "session-token"
	Sign(req, []byte("{}"), creds, "us-east-1", "dynamodb", suiteClock)

	if req.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Error("the session token was not sent")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date;x-amz-security-token,") {
		t.Errorf("the session token is not signed: %s", req.Header.Get("Authorization"))
	}
}

func TestSignChangesWithTheBody(t *testing.T) {
	sign := func(body string) string {
		req, _ := http.NewRequest(http.MethodPost, "https://dynamodb.us-east-1.amazonaws.com/", nil)
		Sign(req, []byte(body), suiteCredentials, "us-east-1", "dynamodb", suiteClock)
		return req.Header.Get("Authorization")
	}
	if sign(`{"a":1}`) == sign(`{"a":2}`) {
		t.Error("two bodies produced the same signature")
	}
}

func TestCanonicalQueryEncodesAsRFC3986(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://sts.amazonaws.com/?b=2&a=x%20y&a=w&c=%2B", nil)
	if got, want := canonicalQuery(req.URL), "a=w&a=x%20y&b=2&c=%2B"; got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}

func TestCredentialsNeverPrint(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIDSECRET", SecretAccessKey: "very-secret", SessionToken: "token-secret"}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "creds", creds)
	for _, rendered := range []string{
		fmt.Sprintf("%v %s %+v %#v", creds, creds, creds, creds),
		logged.String(),
	} {
		for _, secret := range []string{"AKIDSECRET", "very-secret", "token-secret"} {
			if strings.Contains(rendered, secret) {
				t.Errorf("%q printed in %q", secret, rendered)
			}
		}
	}
}
