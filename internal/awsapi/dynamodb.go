package awsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// An Attribute is one DynamoDB attribute value. Only the three kinds the bot
// stores exist: a string, a number and a list.
type Attribute struct {
	kind  string
	value string
	list  []Attribute
}

// S is a string attribute.
func S(value string) Attribute { return Attribute{kind: "S", value: value} }

// N is a number attribute.
func N(value int64) Attribute { return Attribute{kind: "N", value: strconv.FormatInt(value, 10)} }

// L is a list attribute. An empty list is still a list: it encodes as [] and
// never disappears, which DynamoDB would reject as an attribute with no type.
func L(items ...Attribute) Attribute {
	if items == nil {
		items = []Attribute{}
	}
	return Attribute{kind: "L", list: items}
}

// String returns the value of a string attribute.
func (a Attribute) String() (string, bool) { return a.value, a.kind == "S" }

// Int returns the value of a number attribute that holds an integer.
func (a Attribute) Int() (int64, bool) {
	if a.kind != "N" {
		return 0, false
	}
	n, err := strconv.ParseInt(a.value, 10, 64)
	return n, err == nil
}

// List returns the items of a list attribute.
func (a Attribute) List() ([]Attribute, bool) { return a.list, a.kind == "L" }

// MarshalJSON encodes the attribute in DynamoDB's typed form, {"S": "..."}.
func (a Attribute) MarshalJSON() ([]byte, error) {
	switch a.kind {
	case "S", "N":
		return json.Marshal(map[string]string{a.kind: a.value})
	case "L":
		return json.Marshal(map[string][]Attribute{"L": a.list})
	default:
		return nil, errors.New("awsapi: attribute has no value")
	}
}

// UnmarshalJSON decodes the kinds this package knows and rejects the rest, so
// an item written by something else fails loudly rather than reading as empty.
func (a *Attribute) UnmarshalJSON(data []byte) error {
	var typed map[string]json.RawMessage
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	if len(typed) != 1 {
		return fmt.Errorf("awsapi: attribute has %d types, want 1", len(typed))
	}
	for kind, raw := range typed {
		switch kind {
		case "S", "N":
			a.kind = kind
			return json.Unmarshal(raw, &a.value)
		case "L":
			a.kind = kind
			return json.Unmarshal(raw, &a.list)
		default:
			return fmt.Errorf("awsapi: unsupported attribute type %q", kind)
		}
	}
	return nil
}

// An Item is a DynamoDB item, or the key of one.
type Item map[string]Attribute

// Error is an error DynamoDB returned.
type Error struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Type is the exception name, without its namespace:
	// ConditionalCheckFailedException, ResourceNotFoundException, …
	Type    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("dynamodb: %d %s: %s", e.StatusCode, e.Type, e.Message)
}

// IsConditionFailed reports whether err is a write refused by its condition.
func IsConditionFailed(err error) bool {
	var dynamoErr *Error
	return errors.As(err, &dynamoErr) && dynamoErr.Type == "ConditionalCheckFailedException"
}

// DynamoDB is a client for the item operations the announcer's records need.
// Create one with [NewDynamoDB].
type DynamoDB struct {
	endpoint string
	region   string
	creds    Credentials
	http     *http.Client
	now      func() time.Time
}

// A DynamoDBOption adjusts a client built by [NewDynamoDB].
type DynamoDBOption func(*DynamoDB)

// WithEndpoint points the client at another URL, for tests.
func WithEndpoint(endpoint string) DynamoDBOption {
	return func(d *DynamoDB) { d.endpoint = endpoint }
}

// NewDynamoDB returns a client for DynamoDB in region, signing as creds.
func NewDynamoDB(region string, creds Credentials, opts ...DynamoDBOption) *DynamoDB {
	d := &DynamoDB{
		endpoint: "https://dynamodb." + region + ".amazonaws.com/",
		region:   region,
		creds:    creds,
		http:     &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// GetItem reads one item with a strongly consistent read. It returns a nil
// item, and no error, when there is none.
func (d *DynamoDB) GetItem(ctx context.Context, table string, key Item) (Item, error) {
	var out struct {
		Item Item `json:"Item"`
	}
	in := map[string]any{"TableName": table, "Key": key, "ConsistentRead": true}
	if err := d.call(ctx, "GetItem", in, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

// PutItem writes item whole. A non-empty condition is a ConditionExpression,
// and a write it refuses is reported by [IsConditionFailed].
func (d *DynamoDB) PutItem(ctx context.Context, table string, item Item, condition string) error {
	in := map[string]any{"TableName": table, "Item": item}
	if condition != "" {
		in["ConditionExpression"] = condition
	}
	return d.call(ctx, "PutItem", in, nil)
}

// call sends one operation of DynamoDB's JSON protocol.
func (d *DynamoDB) call(ctx context.Context, operation string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("dynamodb %s: encode: %w", operation, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dynamodb %s: %w", operation, err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+operation)
	Sign(req, body, d.creds, d.region, "dynamodb", d.now())

	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("dynamodb %s: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("dynamodb %s: read: %w", operation, err)
	}

	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
			Upper   string `json:"Message"`
		}
		_ = json.Unmarshal(payload, &failure)
		message := failure.Message
		if message == "" {
			message = failure.Upper
		}
		// The type arrives as com.amazonaws.dynamodb.v20120810#Name.
		name := failure.Type[strings.LastIndex(failure.Type, "#")+1:]
		return fmt.Errorf("dynamodb %s: %w", operation, &Error{StatusCode: resp.StatusCode, Type: name, Message: message})
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("dynamodb %s: decode: %w", operation, err)
	}
	return nil
}
