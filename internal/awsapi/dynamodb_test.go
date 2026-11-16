package awsapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttributesRoundTrip(t *testing.T) {
	item := Item{
		"pk":    S("calendar#community"),
		"floor": N(42),
		"seen":  L(S("community/a"), S("community/b")),
		"none":  L(),
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"none":{"L":[]}`) {
		t.Errorf("an empty list did not encode as []: %s", encoded)
	}

	var decoded Item
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if pk, ok := decoded["pk"].String(); !ok || pk != "calendar#community" {
		t.Errorf("pk = %q, %v", pk, ok)
	}
	if floor, ok := decoded["floor"].Int(); !ok || floor != 42 {
		t.Errorf("floor = %d, %v", floor, ok)
	}
	seen, ok := decoded["seen"].List()
	if !ok || len(seen) != 2 {
		t.Fatalf("seen = %v, %v", seen, ok)
	}
	if id, _ := seen[1].String(); id != "community/b" {
		t.Errorf("seen[1] = %q", id)
	}
}

func TestAttributeRejectsUnknownTypes(t *testing.T) {
	var a Attribute
	if err := json.Unmarshal([]byte(`{"SS":["a"]}`), &a); err == nil {
		t.Error("a string set decoded without error")
	}
}

// dynamoStub answers like DynamoDB and records the last request.
func dynamoStub(t *testing.T, status int, response string) (*DynamoDB, *http.Request, *[]byte) {
	t.Helper()
	var (
		seen = new(http.Request)
		body = new([]byte)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = *r.Clone(r.Context())
		*body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	client := NewDynamoDB("us-east-1", Credentials{AccessKeyID: "AKID", SecretAccessKey: "secret", SessionToken: "token"},
		WithEndpoint(server.URL+"/"))
	return client, seen, body
}

func TestGetItemSendsASignedConsistentRead(t *testing.T) {
	client, seen, body := dynamoStub(t, http.StatusOK, `{"Item":{"pk":{"S":"k"},"floor":{"N":"3"}}}`)

	item, err := client.GetItem(t.Context(), "table", Item{"pk": S("k")})
	if err != nil {
		t.Fatal(err)
	}
	if floor, _ := item["floor"].Int(); floor != 3 {
		t.Errorf("floor = %d", floor)
	}
	if seen.Header.Get("X-Amz-Target") != "DynamoDB_20120810.GetItem" {
		t.Errorf("X-Amz-Target = %q", seen.Header.Get("X-Amz-Target"))
	}
	if !strings.HasPrefix(seen.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/") {
		t.Errorf("the request is not signed: %q", seen.Header.Get("Authorization"))
	}
	if !strings.Contains(string(*body), `"ConsistentRead":true`) {
		t.Errorf("the read is not consistent: %s", *body)
	}
}

func TestGetItemReportsAMissingItemAsNil(t *testing.T) {
	client, _, _ := dynamoStub(t, http.StatusOK, `{}`)
	item, err := client.GetItem(t.Context(), "table", Item{"pk": S("k")})
	if err != nil || item != nil {
		t.Errorf("GetItem = %v, %v; want nil, nil", item, err)
	}
}

func TestPutItemReportsAFailedCondition(t *testing.T) {
	client, _, body := dynamoStub(t, http.StatusBadRequest,
		`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`)

	err := client.PutItem(t.Context(), "table", Item{"pk": S("k")}, "attribute_not_exists(pk)")
	if !IsConditionFailed(err) {
		t.Errorf("err = %v, want a failed condition", err)
	}
	if !strings.Contains(string(*body), `"ConditionExpression":"attribute_not_exists(pk)"`) {
		t.Errorf("the condition was not sent: %s", *body)
	}
}

func TestOtherErrorsAreNotAFailedCondition(t *testing.T) {
	client, _, _ := dynamoStub(t, http.StatusBadRequest,
		`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found"}`)
	err := client.PutItem(t.Context(), "table", Item{"pk": S("k")}, "")
	if err == nil || IsConditionFailed(err) {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("the error does not name the exception: %v", err)
	}
}
