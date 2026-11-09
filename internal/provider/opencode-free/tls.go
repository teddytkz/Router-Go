package opencodefree

import (
	"context"
	"crypto/tls"
	_ "embed"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// hello is the TLS ClientHello of the OpenCode CLI (Bun/BoringSSL), captured from the installed client. The free
// gate compares it; an OpenSSL hello is answered with 429 FreeUsageLimitError no matter the headers.
//
//go:embed hello.bin
var hello []byte

// transport is the provider's Transport hook: the CLI's TLS fingerprint on https requests, the plain transport when
// the capture cannot be parsed (fail open rather than refusing to connect).
func transport() *http.Transport { return transportFor(false) }

// transportFor keeps the insecure switch for tests against a local TLS server.
func transportFor(insecure bool) *http.Transport {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
	}
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// The spec is derived per dial: utls consumes the extension list it is handed, so a cached *ClientHelloSpec
		// fails every handshake after the first with "tls: internal error".
		spec, err := (&utls.Fingerprinter{}).FingerprintClientHello(hello)
		if err != nil { // an unparsable capture leaves the plain client in place rather than breaking every call
			host, _, _ := net.SplitHostPort(addr)
			d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{ServerName: host, InsecureSkipVerify: insecure}}
			return d.DialContext(ctx, network, addr)
		}
		d, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(addr)
		u := utls.UClient(d, &utls.Config{ServerName: host, InsecureSkipVerify: insecure}, utls.HelloCustom)
		if err := u.ApplyPreset(spec); err != nil {
			d.Close()
			return nil, err
		}
		if err := u.HandshakeContext(ctx); err != nil {
			d.Close()
			return nil, err
		}
		return u, nil
	}
	return tr
}
