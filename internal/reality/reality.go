// Package reality generates the key material one VLESS+REALITY server needs.
//
// It is generated here rather than by running `xray x25519` on the server, for
// two reasons. The private key never has to be read back over a connection we
// would then have to trust, and a server that fails halfway through its
// bootstrap leaves nothing behind worth keeping - the next attempt makes a
// fresh set, which is the same thing a rotation does.
package reality

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// shortIDBytes is the length of the pre-shared id a client sends with its
// handshake. Xray takes 0 to 8 bytes as hex; 8 is the most it allows and costs
// nothing, and an empty one would let anybody who knows the public key in.
const shortIDBytes = 8

// Material is one server's set. Everything here is generated together and
// belongs together: a client presenting a public key with the wrong short id
// is turned away exactly like one presenting neither.
type Material struct {
	// UUID identifies the single client this server accepts.
	UUID string
	// PrivateKey stays on the server. PublicKey is what a client presents.
	// Both are base64url without padding, which is the form Xray reads.
	PrivateKey string
	PublicKey  string
	// ShortID is hex, and is checked against the server's list on connect.
	ShortID string
}

// New generates a complete set.
func New() (Material, error) {
	id, err := uuid()
	if err != nil {
		return Material{}, err
	}

	private, public, err := keypair()
	if err != nil {
		return Material{}, err
	}

	short, err := shortID()
	if err != nil {
		return Material{}, err
	}

	return Material{UUID: id, PrivateKey: private, PublicKey: public, ShortID: short}, nil
}

// keypair returns an X25519 private and public key, base64url encoded.
func keypair() (private, public string, err error) {
	var key [curve25519.ScalarSize]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", "", fmt.Errorf("generating a REALITY key: %w", err)
	}

	// Clamping is what makes the scalar a valid X25519 private key. Doing it
	// here rather than leaving it to the multiplication means the bytes stored
	// are the same bytes the server will use.
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64

	pub, err := curve25519.X25519(key[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("deriving a REALITY public key: %w", err)
	}

	return encode(key[:]), encode(pub), nil
}

// shortID returns a random pre-shared id as hex.
func shortID() (string, error) {
	var id [shortIDBytes]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generating a REALITY short id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// uuid returns a random (version 4) UUID in the usual text form.
func uuid() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generating a client UUID: %w", err)
	}

	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

// encode renders a key the way Xray does: base64url with no padding.
func encode(key []byte) string {
	return base64.RawURLEncoding.EncodeToString(key)
}
