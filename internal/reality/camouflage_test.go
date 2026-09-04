package reality

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// serve starts a TLS server with a certificate chain of roughly the given
// size, so the size check can be tested against something real rather than a
// hand-written ConnectionState.
func serve(t *testing.T, chain int, alpn []string, maxVersion uint16) string {
	t.Helper()

	certificate := generate(t, chain)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate},
		NextProtos:   alpn,
		MaxVersion:   maxVersion,
	})
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.(*tls.Conn).HandshakeContext(context.Background())
			}()
		}
	}()

	return listener.Addr().String()
}

// generate makes a self-signed certificate, padded out with a filler
// extension so a test can ask for a chain of a particular size.
func generate(t *testing.T, padding int) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "camouflage.example"},
		DNSNames:     []string{"camouflage.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	if padding > 0 {
		// An extension nothing reads, purely to make the certificate the size
		// the test is about.
		template.ExtraExtensions = []pkix.Extension{{
			Id:    []int{1, 2, 3, 4},
			Value: make([]byte, padding),
		}}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// check runs Check against a local server, which needs the certificate
// verification a real site would get to be skipped.
func check(t *testing.T, address string) (Camouflage, error) {
	t.Helper()

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("splitting %q: %v", address, err)
	}
	_ = port

	// Check dials host:443; the test server is on a random port, so the
	// connection is made here and handed to the same inspection.
	conn, err := tls.Dial("tcp", address, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
	})
	if err != nil {
		return Camouflage{}, err
	}
	defer conn.Close()

	state := conn.ConnectionState()
	found := Camouflage{
		Handshake: certificateRecord(state),
		TLS13:     state.Version == tls.VersionTLS13,
		HTTP2:     state.NegotiatedProtocol == "h2",
	}
	return found, verdict(found, "camouflage.example")
}

func TestCheckAcceptsASmallCertificate(t *testing.T) {
	found, err := check(t, serve(t, 0, []string{"h2"}, tls.VersionTLS13))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !found.TLS13 || !found.HTTP2 {
		t.Errorf("got %+v, want TLS 1.3 and HTTP/2", found)
	}
	if found.Handshake > handshakeLimit-handshakeMargin {
		t.Errorf("a bare certificate measured %d bytes", found.Handshake)
	}
}

// This is the failure that cost a working server: the client authenticates,
// the server relays a certificate too big for REALITY's buffer, and the
// handshake dies with nothing useful said on either side.
func TestCheckRefusesACertificateTooBigToRelay(t *testing.T) {
	found, err := check(t, serve(t, handshakeLimit, []string{"h2"}, tls.VersionTLS13))
	if !errors.Is(err, ErrUnsuitable) {
		t.Fatalf("got %v, want ErrUnsuitable", err)
	}
	if found.Handshake <= handshakeLimit {
		t.Errorf("the oversized certificate measured only %d bytes", found.Handshake)
	}
	// The message has to say what is wrong, or the next question is why a
	// perfectly ordinary site was turned down.
	for _, want := range []string{"certificate", "authenticate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCheckRefusesWithoutTLS13(t *testing.T) {
	_, err := check(t, serve(t, 0, []string{"h2"}, tls.VersionTLS12))
	if !errors.Is(err, ErrUnsuitable) {
		t.Fatalf("got %v, want ErrUnsuitable", err)
	}
	if !strings.Contains(err.Error(), "TLS 1.3") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

func TestCheckRefusesWithoutHTTP2(t *testing.T) {
	_, err := check(t, serve(t, 0, []string{"http/1.1"}, tls.VersionTLS13))
	if !errors.Is(err, ErrUnsuitable) {
		t.Fatalf("got %v, want ErrUnsuitable", err)
	}
	if !strings.Contains(err.Error(), "HTTP/2") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

// A site that cannot be reached is the network's fault, not the site's, and
// the caller treats the two differently.
func TestCheckReportsAnUnreachableSiteAsSuch(t *testing.T) {
	// Reserved for documentation, so nothing answers on it.
	_, err := Check(context.Background(), "unreachable.invalid")
	if err == nil {
		t.Fatal("expected an error for a site that does not resolve")
	}
	if errors.Is(err, ErrUnsuitable) {
		t.Errorf("an unreachable site was called unsuitable: %v", err)
	}
}

func TestCheckGivesUpWhenTheContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Check(ctx, "www.apple.com"); err == nil {
		t.Fatal("expected an error for a canceled context")
	}
}
