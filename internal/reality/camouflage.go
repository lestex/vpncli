package reality

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// handshakeLimit is the largest TLS handshake record REALITY can relay. It is
// `size` in xtls/reality, which reads the camouflage site's response into a
// buffer of exactly this many bytes and gives up on a record that does not
// fit.
//
// The record that gets near it is always the Certificate one: a long chain,
// an OCSP staple and a few signed certificate timestamps add up faster than
// they look. A site over the limit authenticates our clients perfectly and
// then fails to finish the handshake, which is as confusing a failure as this
// program can produce - so it is checked before a server is built around it
// rather than discovered afterwards.
const handshakeLimit = 8192

// handshakeMargin is how much room is left under the limit. The size is
// measured from here and used by the server, which may be handed a different
// certificate by a CDN that answers per region, and a site sitting a hundred
// bytes under the limit today is one renewal away from being over it.
const handshakeMargin = 1024

// port is where a camouflage site is impersonated, and so where it has to be
// inspected. It is 443 for the same reason the server listens there.
const port = "443"

// checkTimeout bounds one candidate. This runs inside the wizard, where a
// site that is slow to answer should not hold up a question.
const checkTimeout = 10 * time.Second

// ErrUnsuitable is returned for a host that cannot serve as camouflage.
var ErrUnsuitable = errors.New("unsuitable camouflage")

// Camouflage is what one candidate site offers.
type Camouflage struct {
	// Handshake is the estimated size of the Certificate record the server
	// would have to relay.
	Handshake int
	// TLS13 and HTTP2 report what the site negotiated.
	TLS13 bool
	HTTP2 bool
}

// Check connects to a candidate camouflage site and reports whether REALITY
// can hide behind it.
//
// A site has to speak TLS 1.3, because that is the handshake being imitated;
// offer HTTP/2, because a site that does not is unusual enough to be worth
// noticing; and present a certificate small enough to relay.
//
// The vantage point is this machine rather than the server, which is the one
// weakness here: a CDN can answer differently in another region. The margin
// under the limit is what that buys tolerance for.
func Check(ctx context.Context, host string) (Camouflage, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	dialer := tls.Dialer{Config: &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}}

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return Camouflage{}, fmt.Errorf("reaching %s: %w", host, err)
	}
	defer conn.Close()

	found := inspect(conn.(*tls.Conn).ConnectionState())
	return found, verdict(found, host)
}

// inspect reads what a finished handshake says about the site.
func inspect(state tls.ConnectionState) Camouflage {
	return Camouflage{
		Handshake: certificateRecord(state),
		TLS13:     state.Version == tls.VersionTLS13,
		HTTP2:     state.NegotiatedProtocol == "h2",
	}
}

// verdict decides whether a site can be hidden behind, and says why not.
func verdict(found Camouflage, host string) error {
	switch {
	case !found.TLS13:
		return fmt.Errorf("%w: %s does not speak TLS 1.3, which is the handshake REALITY imitates", ErrUnsuitable, host)
	case !found.HTTP2:
		return fmt.Errorf("%w: %s does not offer HTTP/2, which makes it an odd site to be seen visiting", ErrUnsuitable, host)
	case found.Handshake > handshakeLimit-handshakeMargin:
		return fmt.Errorf("%w: %s sends a %d byte certificate and REALITY can only relay %d - clients would authenticate and then fail at the handshake",
			ErrUnsuitable, host, found.Handshake, handshakeLimit)
	}
	return nil
}

// certificateRecord estimates the Certificate handshake record the site sends.
//
// It is assembled rather than measured, because crypto/tls hands back the
// parsed pieces and not the bytes: the record and handshake headers, then each
// certificate with its length and extensions, then the staple and timestamps
// that ride along with the first one. Measured against a site known to be over
// the limit it lands within a hundred bytes, which is what the margin is for.
func certificateRecord(state tls.ConnectionState) int {
	const (
		recordHeader    = 5 // type, version, length
		handshakeHeader = 4 // type, length
		requestContext  = 1 // always empty from a server
		listLength      = 3
		certLength      = 3
		extensionsLen   = 2
	)

	size := recordHeader + handshakeHeader + requestContext + listLength
	for _, certificate := range state.PeerCertificates {
		size += certLength + len(certificate.Raw) + extensionsLen
	}

	// Both of these are extensions on the leaf certificate.
	if len(state.OCSPResponse) > 0 {
		size += 4 + 3 + len(state.OCSPResponse) + 1
	}
	if len(state.SignedCertificateTimestamps) > 0 {
		size += 4 + 2
		for _, timestamp := range state.SignedCertificateTimestamps {
			size += 2 + len(timestamp)
		}
	}
	return size
}
