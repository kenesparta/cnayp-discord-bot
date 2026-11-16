package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
)

// The headers Discord signs every interaction request with.
const (
	HeaderSignature = "X-Signature-Ed25519"
	HeaderTimestamp = "X-Signature-Timestamp"
)

// ParsePublicKey decodes the application's public key as the Developer Portal
// shows it: 64 hexadecimal characters.
func ParsePublicKey(hexKey string) (ed25519.PublicKey, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("public key is not hexadecimal: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(key), nil
}

// ErrBadSignature is a request Discord did not sign.
var ErrBadSignature = errors.New("interaction signature does not verify")

// VerifyInteraction checks that body came from Discord: the signature header
// must be key's Ed25519 signature over the timestamp header followed by the
// body, exactly as received.
//
// Every request to the interactions endpoint has to pass this before anything
// else reads it. Discord tests the endpoint when it is saved by sending a
// request whose signature is wrong on purpose, and refuses an endpoint that
// does not answer it with 401.
func VerifyInteraction(key ed25519.PublicKey, signature, timestamp string, body []byte) error {
	sig, err := hex.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize || timestamp == "" {
		return ErrBadSignature
	}
	message := make([]byte, 0, len(timestamp)+len(body))
	message = append(message, timestamp...)
	message = append(message, body...)
	if !ed25519.Verify(key, message, sig) {
		return ErrBadSignature
	}
	return nil
}
