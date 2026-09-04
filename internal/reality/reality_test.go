package reality

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestNew(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if !uuidPattern.MatchString(m.UUID) {
		t.Errorf("uuid = %q, want a version 4 UUID", m.UUID)
	}

	// Xray reads these as base64url without padding. Anything else is a config
	// the server refuses to start on.
	for name, key := range map[string]string{"private": m.PrivateKey, "public": m.PublicKey} {
		raw, err := base64.RawURLEncoding.DecodeString(key)
		if err != nil {
			t.Errorf("%s key %q is not base64url: %v", name, key, err)
			continue
		}
		if len(raw) != curve25519.ScalarSize {
			t.Errorf("%s key is %d bytes, want %d", name, len(raw), curve25519.ScalarSize)
		}
	}

	if _, err := hex.DecodeString(m.ShortID); err != nil {
		t.Errorf("short id %q is not hex: %v", m.ShortID, err)
	}
	if len(m.ShortID) != 2*shortIDBytes {
		t.Errorf("short id %q is %d characters, want %d", m.ShortID, len(m.ShortID), 2*shortIDBytes)
	}
}

// The client presents the public half of the key the server holds. If these
// two ever stopped being a pair, every handshake would fail with nothing on
// either side saying why.
func TestKeypairIsAPair(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	private, err := base64.RawURLEncoding.DecodeString(m.PrivateKey)
	if err != nil {
		t.Fatalf("decoding the private key: %v", err)
	}

	derived, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("deriving the public key: %v", err)
	}
	if got := encode(derived); got != m.PublicKey {
		t.Errorf("public key = %q, want %q derived from the private key", m.PublicKey, got)
	}
}

// The stored private key has to be the clamped one, or the server and this
// package would disagree about which key it is.
func TestPrivateKeyIsClamped(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	key, err := base64.RawURLEncoding.DecodeString(m.PrivateKey)
	if err != nil {
		t.Fatalf("decoding the private key: %v", err)
	}
	if key[0]&7 != 0 || key[31]&128 != 0 || key[31]&64 == 0 {
		t.Errorf("private key %x is not clamped", key)
	}
}

// Every server gets its own set. Reusing one would tie two servers together,
// which is the whole thing rotation is meant to prevent.
func TestNewIsDifferentEveryTime(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if first == second {
		t.Fatal("two servers were given the same material")
	}
	if first.UUID == second.UUID || first.PrivateKey == second.PrivateKey || first.ShortID == second.ShortID {
		t.Errorf("something was reused:\n%+v\n%+v", first, second)
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
