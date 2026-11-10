package opencodefree

import (
	"bufio"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

// hello is the TLS ClientHello of the OpenCode CLI (Bun/BoringSSL), captured from the installed client. The free
// gate compares it; an OpenSSL hello is answered with 429 FreeUsageLimitError no matter the headers.
//
//go:embed hello.bin
var hello []byte

// transport is the provider's Transport hook. Proxies are supported as an HTTP CONNECT tunnel: TLS then runs end to
// end to opencode.ai, so the CLI's fingerprint is what the gate sees. Relay pools cannot carry this provider.
func transport() *http.Transport { return transportFor(false) }

// transportFor keeps the insecure switch for tests against a local TLS server.
func transportFor(insecure bool) *http.Transport {
	tr := &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
	}
	// DialTLSContext takes the connection through the proxy's CONNECT tunnel and runs the CLI's ClientHello inside it,
	// so the returned conn is already TLS. The transport's own Proxy field stays nil: with it set, net/http would hand
	// this hook the proxy's address and the fingerprint would go to the proxy instead of to opencode.ai.
	// shortcut: HTTP(S)_PROXY in the environment is ignored for this provider (a proxy pool is the supported path),
	// upgrade if a deployment has no pool but does have the env vars set.
	tr.DialTLSContext = dialViaProxy(insecure)
	return tr
}

// proxyKey carries the pool's proxy URL from the gateway to the dialer (see ProxyForConn).
type proxyKey struct{}

// ProxyForConn names the proxy for the request's connection. The gateway calls it before sending a request whose key
// has a proxy pool: this provider signs its own TLS, so it cannot go through the pool transport.
func ProxyForConn(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, proxyKey{}, raw)
}

// dialViaProxy connects to the target through the request's proxy (CONNECT) and fingerprints the TLS inside it; with
// no proxy on the context it dials the target directly.
func dialViaProxy(insecure bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	fp := &fingerprintDialer{insecure: insecure}
	var mu sync.Mutex
	tunnels := map[string]*proxyDialer{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, _ := ctx.Value(proxyKey{}).(string)
		if raw == "" {
			return fp.dial(ctx, network, addr)
		}
		px, err := url.Parse(raw)
		if err != nil || px.Host == "" {
			return nil, errors.New("invalid proxy URL")
		}
		if px.Scheme != "http" && px.Scheme != "https" {
			return nil, errors.New("this provider needs an http(s) proxy: a socks5 proxy cannot carry its TLS fingerprint")
		}
		mu.Lock()
		d := tunnels[raw]
		if d == nil {
			d = newProxyDialer(px, fp)
			tunnels[raw] = d
		}
		mu.Unlock()
		return d.dial(ctx, network, addr)
	}
}

type fingerprintDialer struct{ insecure bool }

func (f *fingerprintDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	// The spec is derived per dial: utls consumes the extension list it is handed, so a cached *ClientHelloSpec
	// fails every handshake after the first with "tls: internal error".
	spec, err := (&utls.Fingerprinter{}).FingerprintClientHello(hello)
	if err != nil { // an unparsable capture leaves the plain client in place rather than breaking every call
		host, _, _ := net.SplitHostPort(addr)
		d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{ServerName: host, InsecureSkipVerify: f.insecure}}
		return d.DialContext(ctx, network, addr)
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return handshake(ctx, conn, addr, spec, f.insecure)
}

// handshake runs the CLI's ClientHello over conn and returns the TLS connection.
func handshake(ctx context.Context, conn net.Conn, addr string, spec *utls.ClientHelloSpec, insecure bool) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)
	u := utls.UClient(conn, &utls.Config{ServerName: host, InsecureSkipVerify: insecure}, utls.HelloCustom)
	if err := u.ApplyPreset(spec); err != nil {
		conn.Close()
		return nil, err
	}
	if err := u.HandshakeContext(ctx); err != nil {
		err = fmt.Errorf("TLS handshake to %s: %w", host, err)
		conn.Close()
		return nil, err
	}
	return u, nil
}

// proxyDialer opens a CONNECT tunnel through one HTTP proxy and fingerprints the TLS inside it.
type proxyDialer struct {
	addr string      // proxy "host:port"
	auth string      // Proxy-Authorization, empty when the proxy needs none
	tls  *tls.Config // non-nil when the proxy leg itself is TLS (https:// proxy)
	ins  bool
}

func newProxyDialer(px *url.URL, fp *fingerprintDialer) *proxyDialer {
	d := &proxyDialer{ins: fp.insecure}
	port := px.Port()
	if port == "" {
		port = "80"
		if px.Scheme == "https" {
			port = "443"
		}
	}
	d.addr = net.JoinHostPort(px.Hostname(), port)
	if px.User != nil {
		p, _ := px.User.Password()
		d.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(px.User.Username()+":"+p))
	}
	if px.Scheme == "https" {
		d.tls = &tls.Config{ServerName: px.Hostname()}
	}
	return d
}

func (d *proxyDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	// Derive the spec per dial, like fingerprintDialer: utls consumes the extension list it is handed, so a cached
	// spec fails every handshake after the first ("tls: internal error").
	spec, specErr := (&utls.Fingerprinter{}).FingerprintClientHello(hello)
	nd := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := nd.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", d.addr, err)
	}
	if d.tls != nil {
		td := &tls.Dialer{NetDialer: nd, Config: d.tls}
		conn, err = td.DialContext(ctx, "tcp", d.addr)
		if err != nil {
			return nil, fmt.Errorf("proxy %s (TLS): %w", d.addr, err)
		}
	}
	if err := d.connect(ctx, conn, addr); err != nil {
		conn.Close()
		return nil, err
	}
	if specErr != nil { // the capture could not be parsed: fail open with a plain handshake
		return plainHandshake(ctx, conn, addr, d.ins)
	}
	return handshake(ctx, conn, addr, spec, d.ins)
}

// connect issues "CONNECT host:port" and reads the proxy's reply.
func (d *proxyDialer) connect(ctx context.Context, conn net.Conn, addr string) error {
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if d.auth != "" {
		req += "Proxy-Authorization: " + d.auth + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
		return fmt.Errorf("proxy %s: %w", d.addr, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		return fmt.Errorf("proxy %s: %w", d.addr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("proxy %s refused the tunnel: %s", d.addr, resp.Status)
	}
	if br.Buffered() > 0 { // the proxy answered with extra bytes; they belong to the tunnel and are lost
		return errors.New("proxy " + d.addr + " sent data before the tunnel was established")
	}
	return conn.SetDeadline(time.Time{})
}

func plainHandshake(ctx context.Context, conn net.Conn, addr string, insecure bool) (net.Conn, error) {
	host, _, _ := net.SplitHostPort(addr)
	td := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: insecure})
	if err := td.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return td, nil
}
