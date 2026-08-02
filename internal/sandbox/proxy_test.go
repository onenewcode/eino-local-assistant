package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProxyAllowsOnlyExactPublicHostnames(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Host != "allowed.test" {
			t.Errorf("upstream Host = %q, want allowed.test", request.Host)
		}
		_, _ = io.WriteString(w, "allowed response")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dialCount atomic.Int32
	proxy, err := startHTTPProxyWithDial(listener, []string{"allowed.test"}, "", func(ctx context.Context, host, port string) (net.Conn, error) {
		dialCount.Add(1)
		if host != "allowed.test" || port != "80" {
			return nil, fmt.Errorf("proxy dial = %s:%s, want allowed.test:80", host, port)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstreamURL.Host)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	response, err := client.Get("http://allowed.test/ok")
	if err != nil {
		t.Fatalf("allowed request: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "allowed response" {
		t.Fatalf("allowed response = %d %q", response.StatusCode, body)
	}
	if got := dialCount.Load(); got != 1 {
		t.Fatalf("allowed dial count = %d, want 1", got)
	}

	for _, target := range []string{"http://blocked.test/", "http://127.0.0.1/"} {
		response, err := client.Get(target)
		if err != nil {
			t.Fatalf("blocked request %q: %v", target, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("blocked request %q status = %d, want %d", target, response.StatusCode, http.StatusForbidden)
		}
	}
	if got := dialCount.Load(); got != 1 {
		t.Fatalf("blocked requests reached upstream %d times", got)
	}
}

func TestHTTPProxyRejectsMismatchedHTTPHostHeader(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dialCount atomic.Int32
	proxy, err := startHTTPProxyWithDial(listener, []string{"allowed.test"}, "", func(context.Context, string, string) (net.Conn, error) {
		dialCount.Add(1)
		return nil, errors.New("unexpected upstream dial")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	client, server := net.Pipe()
	defer client.Close()
	go proxy.serveHTTP(server, &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "http", Host: "allowed.test", Path: "/"},
		Host:   "blocked.test",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewReader(nil)),
	})
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched Host status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("mismatched Host reached upstream %d times", got)
	}
}

func TestHTTPProxyConnectRequiresMatchingSNI(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamReads := make(chan int, 1)
	proxy, err := startHTTPProxyWithDial(listener, []string{"allowed.test"}, "", func(context.Context, string, string) (net.Conn, error) {
		proxySide, upstreamSide := net.Pipe()
		go func() {
			defer upstreamSide.Close()
			_ = upstreamSide.SetReadDeadline(time.Now().Add(time.Second))
			buffer := make([]byte, 1024)
			n, _ := upstreamSide.Read(buffer)
			upstreamReads <- n
		}()
		return proxySide, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := io.WriteString(client, "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if _, err := client.Write(tlsClientHello(t, "blocked.test")); err != nil {
		t.Fatalf("write mismatched ClientHello: %v", err)
	}
	if n := <-upstreamReads; n != 0 {
		t.Fatalf("mismatched SNI forwarded %d bytes upstream", n)
	}
}

func TestReadTLSClientHelloExtractsSNI(t *testing.T) {
	hello := tlsClientHello(t, "ALLOWED.test")
	_, serverName, err := readTLSClientHello(bufio.NewReader(bytes.NewReader(hello)))
	if err != nil {
		t.Fatalf("readTLSClientHello() error = %v", err)
	}
	if serverName != "allowed.test" {
		t.Fatalf("SNI = %q, want allowed.test", serverName)
	}
}

func TestReadTLSClientHelloRejectsEncryptedClientHello(t *testing.T) {
	hello := appendTLSClientHelloExtension(t, tlsClientHello(t, "allowed.test"), tlsExtensionEncryptedClientHello, nil)
	_, _, err := readTLSClientHello(bufio.NewReader(bytes.NewReader(hello)))
	if err == nil || !strings.Contains(err.Error(), "encrypted client hello") {
		t.Fatalf("readTLSClientHello() error = %v, want encrypted ClientHello rejection", err)
	}
}

func appendTLSClientHelloExtension(t *testing.T, hello []byte, typ uint16, value []byte) []byte {
	t.Helper()
	if len(hello) < 9 || hello[0] != 22 || hello[5] != 1 {
		t.Fatal("invalid TLS ClientHello fixture")
	}
	bodyOffset := 9 // TLS record header + handshake type/length.
	offset := bodyOffset + 34
	if offset >= len(hello) {
		t.Fatal("truncated TLS ClientHello fixture")
	}
	sessionLength := int(hello[offset])
	offset += 1 + sessionLength
	if offset+2 > len(hello) {
		t.Fatal("truncated TLS ClientHello cipher suites")
	}
	cipherLength := int(hello[offset])<<8 | int(hello[offset+1])
	offset += 2 + cipherLength
	if offset >= len(hello) {
		t.Fatal("truncated TLS ClientHello compression methods")
	}
	compressionLength := int(hello[offset])
	offset += 1 + compressionLength
	if offset+2 > len(hello) {
		t.Fatal("truncated TLS ClientHello extensions")
	}
	extensionLengthOffset := offset
	extensionLength := int(hello[offset])<<8 | int(hello[offset+1])
	offset += 2
	if offset+extensionLength != len(hello) {
		t.Fatal("unexpected TLS ClientHello record framing")
	}

	extension := make([]byte, 4+len(value))
	extension[0] = byte(typ >> 8)
	extension[1] = byte(typ)
	extension[2] = byte(len(value) >> 8)
	extension[3] = byte(len(value))
	copy(extension[4:], value)
	hello = append(hello, extension...)

	updatedExtensions := extensionLength + len(extension)
	hello[extensionLengthOffset] = byte(updatedExtensions >> 8)
	hello[extensionLengthOffset+1] = byte(updatedExtensions)
	updatedHandshake := (len(hello) - 5) - 4
	hello[6] = byte(updatedHandshake >> 16)
	hello[7] = byte(updatedHandshake >> 8)
	hello[8] = byte(updatedHandshake)
	updatedRecord := len(hello) - 5
	hello[3] = byte(updatedRecord >> 8)
	hello[4] = byte(updatedRecord)
	return hello
}

func tlsClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	client, server := net.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- tls.Client(client, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}).Handshake()
	}()

	header := make([]byte, 5)
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatalf("read TLS record header: %v", err)
	}
	length := int(header[3])<<8 | int(header[4])
	payload := make([]byte, length)
	if _, err := io.ReadFull(server, payload); err != nil {
		t.Fatalf("read TLS record payload: %v", err)
	}
	_ = client.Close()
	_ = server.Close()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("TLS client did not stop after ClientHello capture")
	}
	return append(header, payload...)
}

func TestHTTPProxyCloseClosesActiveConnectTunnel(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	upstreamDone := make(chan net.Conn, 1)
	proxy, err := startHTTPProxyWithDial(listener, []string{"allowed.test"}, "", func(context.Context, string, string) (net.Conn, error) {
		client, upstream := net.Pipe()
		upstreamDone <- upstream
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := io.WriteString(client, "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	upstream := <-upstreamDone
	defer upstream.Close()

	done := make(chan error, 1)
	go func() { done <- proxy.Close() }()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() blocked on an active CONNECT tunnel")
	}
}

func TestUnsafeProxyIPRejectsSpecialUseRanges(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0",
		"100.64.0.1",
		"192.0.0.1",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"2001:db8::1",
		"2001:2::1",
		"64:ff9b:1::1",
		"64:ff9b::a00:1",
		"fec0::1",
		"::ffff:198.51.100.1",
		"::192.0.2.1",
	} {
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !unsafeProxyIP(ip) {
			t.Errorf("unsafeProxyIP(%s) = false, want true", raw)
		}
	}

	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		ip, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatal(err)
		}
		if unsafeProxyIP(ip) {
			t.Errorf("unsafeProxyIP(%s) = true, want false", raw)
		}
	}
}
