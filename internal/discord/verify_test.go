package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
)

func TestVerifyInteraction(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const timestamp = "1731758400"
	body := []byte(`{"type":1}`)
	signature := hex.EncodeToString(ed25519.Sign(private, append([]byte(timestamp), body...)))

	if err := VerifyInteraction(public, signature, timestamp, body); err != nil {
		t.Errorf("a genuine request was refused: %v", err)
	}

	otherPublic, _, _ := ed25519.GenerateKey(nil)
	for name, attempt := range map[string]func() error{
		"tampered body":     func() error { return VerifyInteraction(public, signature, timestamp, []byte(`{"type":2}`)) },
		"other timestamp":   func() error { return VerifyInteraction(public, signature, "1731758401", body) },
		"no timestamp":      func() error { return VerifyInteraction(public, signature, "", body) },
		"not hexadecimal":   func() error { return VerifyInteraction(public, "zz", timestamp, body) },
		"empty signature":   func() error { return VerifyInteraction(public, "", timestamp, body) },
		"another app's key": func() error { return VerifyInteraction(otherPublic, signature, timestamp, body) },
	} {
		if err := attempt(); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	public, _, _ := ed25519.GenerateKey(nil)
	key, err := ParsePublicKey(hex.EncodeToString(public))
	if err != nil || !key.Equal(public) {
		t.Errorf("ParsePublicKey = %v, %v", key, err)
	}
	for _, bad := range []string{"", "abc", hex.EncodeToString(public[:31]), "zz" + hex.EncodeToString(public)[2:]} {
		if _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("ParsePublicKey(%q) accepted a bad key", bad)
		}
	}
}
