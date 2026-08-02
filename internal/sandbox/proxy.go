package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPProxy is a loopback-only egress proxy for sandbox workers. It accepts
// only HTTP on port 80 and HTTPS CONNECT on port 443 for exact allowlisted
// names. The worker's OS sandbox must still prohibit every other socket.
type HTTPProxy struct {
	listener     net.Listener
	allowed      map[string]struct{}
	transport    *http.Transport
	socketPath   string
	dialOverride proxyDialFunc
	ctx          context.Context
	cancel       context.CancelFunc

	closeOnce  sync.Once
	acceptDone chan struct{}
	connWG     sync.WaitGroup
	connMu     sync.Mutex
	conns      map[net.Conn]struct{}
	closing    bool
}

type proxyDialFunc func(context.Context, string, string) (net.Conn, error)

// StartHTTPProxy starts a loopback proxy for the supplied exact host names.
// An empty allowlist is not a proxy with open access; callers should keep
// network disabled instead.
func StartHTTPProxy(allowedHosts []string) (*HTTPProxy, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen sandbox proxy: %w", err)
	}
	return startHTTPProxy(listener, allowedHosts, "")
}

// StartUnixHTTPProxy starts the same hostname-filtering proxy on a Unix
// socket. Linux bwrap workers mount this socket and expose it through an
// isolated loopback relay, so the host network is never directly reachable.
func StartUnixHTTPProxy(allowedHosts []string, socketPath string) (*HTTPProxy, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, errors.New("sandbox unix proxy socket path is required")
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale sandbox proxy socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen sandbox unix proxy: %w", err)
	}
	return startHTTPProxy(listener, allowedHosts, socketPath)
}

func startHTTPProxy(listener net.Listener, allowedHosts []string, socketPath string) (*HTTPProxy, error) {
	return startHTTPProxyWithDial(listener, allowedHosts, socketPath, nil)
}

func startHTTPProxyWithDial(listener net.Listener, allowedHosts []string, socketPath string, dialOverride proxyDialFunc) (*HTTPProxy, error) {
	if len(allowedHosts) == 0 {
		_ = listener.Close()
		return nil, errors.New("sandbox proxy requires at least one allowed host")
	}
	normalized, err := normalizeAllowedHosts(allowedHosts)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	allowed := make(map[string]struct{}, len(normalized))
	for _, host := range normalized {
		allowed[host] = struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &HTTPProxy{
		listener:     listener,
		allowed:      allowed,
		socketPath:   socketPath,
		dialOverride: dialOverride,
		acceptDone:   make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		conns:        make(map[net.Conn]struct{}),
	}
	p.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dialContext,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	go p.acceptLoop()
	return p, nil
}

// Port reports the loopback TCP port that the worker sandbox may reach.
func (p *HTTPProxy) Port() int {
	if p == nil || p.listener == nil {
		return 0
	}
	addr, ok := p.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return addr.Port
}

// URL returns the proxy URL suitable for HTTP_PROXY and HTTPS_PROXY.
func (p *HTTPProxy) URL() string {
	if p == nil || p.Port() == 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(p.Port())
}

// Close stops new connections, cancels upstream dials, and closes active
// tunnels before waiting so worker cleanup cannot block on a CONNECT stream.
func (p *HTTPProxy) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		connections := p.beginClose()
		if p.listener != nil {
			err = p.listener.Close()
		}
		if p.acceptDone != nil {
			<-p.acceptDone
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
		if p.socketPath != "" {
			_ = os.Remove(p.socketPath)
		}
		p.transport.CloseIdleConnections()
		p.connWG.Wait()
	})
	return err
}

func (p *HTTPProxy) acceptLoop() {
	defer close(p.acceptDone)
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if !p.registerConnection(conn) {
			_ = conn.Close()
			continue
		}
		go func() {
			defer p.connWG.Done()
			defer p.unregisterConnection(conn)
			defer conn.Close()
			p.serve(conn)
		}()
	}
}

func (p *HTTPProxy) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	defer req.Body.Close()

	if strings.EqualFold(req.Method, http.MethodConnect) {
		p.serveConnect(conn, reader, req)
		return
	}
	p.serveHTTP(conn, req)
}

func (p *HTTPProxy) serveConnect(client net.Conn, reader *bufio.Reader, req *http.Request) {
	host, port, err := p.authorizeTarget(req.Host, "443", "443")
	if err != nil {
		writeProxyError(client, http.StatusForbidden, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(p.context(), 30*time.Second)
	defer cancel()
	upstream, err := p.dialResolved(ctx, host, port)
	if err != nil {
		writeProxyError(client, http.StatusBadGateway, "upstream unavailable")
		return
	}
	defer upstream.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	clientHello, serverName, err := readTLSClientHello(reader)
	if err != nil || serverName != host {
		// CONNECT is only an HTTPS tunnel. Do not forward arbitrary TCP or a
		// ClientHello whose SNI selects a different virtual host on the same IP.
		return
	}
	if _, err := upstream.Write(clientHello); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, reader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

func (p *HTTPProxy) serveHTTP(client net.Conn, req *http.Request) {
	if req.URL == nil || !strings.EqualFold(req.URL.Scheme, "http") {
		writeProxyError(client, http.StatusForbidden, "only http proxy requests are allowed")
		return
	}
	host, _, err := p.authorizeTarget(req.URL.Host, "80", "80")
	if err != nil {
		writeProxyError(client, http.StatusForbidden, err.Error())
		return
	}
	if req.Host != "" {
		hostHeader, _, err := p.authorizeTarget(req.Host, "80", "80")
		if err != nil || hostHeader != host {
			writeProxyError(client, http.StatusForbidden, "sandbox proxy Host header must match the request target")
			return
		}
	}
	// http.Transport expects a client request, not a proxy request.
	req.RequestURI = ""
	req.Close = false
	// Never let a model-controlled Host header select another virtual host on a
	// shared upstream address after the target authority has been allowlisted.
	req.URL.Host = host
	req.Host = host
	req.Header.Del("Host")
	ctx, cancel := context.WithTimeout(p.context(), 30*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	response, err := p.transport.RoundTrip(req)
	if err != nil {
		writeProxyError(client, http.StatusBadGateway, "upstream unavailable")
		return
	}
	defer response.Body.Close()
	_ = response.Write(client)
}

const (
	maxTLSClientHelloBytes = 64 << 10
	// tlsExtensionEncryptedClientHello is RFC 9337's EncryptedClientHello
	// extension. Its encrypted inner SNI cannot be checked against CONNECT, so
	// a hostname-enforcing proxy must reject it rather than trusting outer SNI.
	tlsExtensionEncryptedClientHello = 0xfe0d
)

// readTLSClientHello returns the raw TLS records needed to forward the first
// ClientHello and its normalized SNI. It rejects non-TLS CONNECT payloads,
// missing SNI, and oversized handshakes rather than treating CONNECT as an
// arbitrary TCP escape hatch.
func readTLSClientHello(reader *bufio.Reader) ([]byte, string, error) {
	if reader == nil {
		return nil, "", errors.New("TLS ClientHello reader is required")
	}

	raw := make([]byte, 0, 4096)
	handshake := make([]byte, 0, 4096)
	for {
		header := make([]byte, 5)
		if _, err := io.ReadFull(reader, header); err != nil {
			return nil, "", fmt.Errorf("read TLS record header: %w", err)
		}
		if header[0] != 22 { // TLS handshake record
			return nil, "", errors.New("CONNECT payload is not a TLS ClientHello")
		}
		recordLength := int(header[3])<<8 | int(header[4])
		if recordLength == 0 || len(raw)+len(header)+recordLength > maxTLSClientHelloBytes {
			return nil, "", errors.New("TLS ClientHello exceeds proxy limit")
		}
		payload := make([]byte, recordLength)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, "", fmt.Errorf("read TLS ClientHello: %w", err)
		}
		raw = append(raw, header...)
		raw = append(raw, payload...)
		handshake = append(handshake, payload...)

		if len(handshake) < 4 {
			continue
		}
		if handshake[0] != 1 { // client_hello
			return nil, "", errors.New("TLS handshake does not begin with ClientHello")
		}
		length := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
		total := 4 + length
		if total > maxTLSClientHelloBytes {
			return nil, "", errors.New("TLS ClientHello exceeds proxy limit")
		}
		if len(handshake) < total {
			continue
		}
		serverName, err := parseTLSClientHelloServerName(handshake[:total])
		if err != nil {
			return nil, "", err
		}
		return raw, serverName, nil
	}
}

func parseTLSClientHelloServerName(clientHello []byte) (string, error) {
	if len(clientHello) < 4 || clientHello[0] != 1 {
		return "", errors.New("invalid TLS ClientHello")
	}
	length := int(clientHello[1])<<16 | int(clientHello[2])<<8 | int(clientHello[3])
	if length != len(clientHello)-4 {
		return "", errors.New("invalid TLS ClientHello length")
	}
	body := clientHello[4:]
	offset := 0
	take := func(size int) ([]byte, error) {
		if size < 0 || offset+size > len(body) {
			return nil, errors.New("truncated TLS ClientHello")
		}
		value := body[offset : offset+size]
		offset += size
		return value, nil
	}

	if _, err := take(34); err != nil { // legacy_version + random
		return "", err
	}
	sessionLength, err := take(1)
	if err != nil {
		return "", err
	}
	if _, err := take(int(sessionLength[0])); err != nil {
		return "", err
	}
	cipherLength, err := take(2)
	if err != nil {
		return "", err
	}
	cipherSuites := int(cipherLength[0])<<8 | int(cipherLength[1])
	if cipherSuites == 0 || cipherSuites%2 != 0 {
		return "", errors.New("invalid TLS ClientHello cipher suites")
	}
	if _, err := take(cipherSuites); err != nil {
		return "", err
	}
	compressionLength, err := take(1)
	if err != nil {
		return "", err
	}
	if _, err := take(int(compressionLength[0])); err != nil {
		return "", err
	}
	extensionsLength, err := take(2)
	if err != nil {
		return "", err
	}
	extensionsSize := int(extensionsLength[0])<<8 | int(extensionsLength[1])
	if extensionsSize != len(body)-offset {
		return "", errors.New("invalid TLS ClientHello extensions")
	}
	extensions := body[offset:]
	var serverName string
	for offset := 0; offset < len(extensions); {
		if offset+4 > len(extensions) {
			return "", errors.New("truncated TLS ClientHello extension")
		}
		typ := int(extensions[offset])<<8 | int(extensions[offset+1])
		size := int(extensions[offset+2])<<8 | int(extensions[offset+3])
		offset += 4
		if offset+size > len(extensions) {
			return "", errors.New("truncated TLS ClientHello extension")
		}
		value := extensions[offset : offset+size]
		offset += size
		if typ == tlsExtensionEncryptedClientHello {
			return "", errors.New("TLS ClientHello uses encrypted client hello")
		}
		if typ != 0 { // server_name
			continue
		}
		if serverName != "" || len(value) < 2 {
			return "", errors.New("invalid TLS ClientHello SNI")
		}
		listLength := int(value[0])<<8 | int(value[1])
		if listLength != len(value)-2 {
			return "", errors.New("invalid TLS ClientHello SNI")
		}
		for nameOffset := 2; nameOffset < len(value); {
			if nameOffset+3 > len(value) {
				return "", errors.New("truncated TLS ClientHello SNI")
			}
			nameType := value[nameOffset]
			nameLength := int(value[nameOffset+1])<<8 | int(value[nameOffset+2])
			nameOffset += 3
			if nameOffset+nameLength > len(value) {
				return "", errors.New("truncated TLS ClientHello SNI")
			}
			if nameType == 0 {
				if serverName != "" {
					return "", errors.New("multiple TLS ClientHello server names")
				}
				name, err := normalizeHost(string(value[nameOffset : nameOffset+nameLength]))
				if err != nil {
					return "", fmt.Errorf("invalid TLS ClientHello SNI: %w", err)
				}
				serverName = name
			}
			nameOffset += nameLength
		}
	}
	if serverName == "" {
		return "", errors.New("TLS ClientHello has no DNS SNI")
	}
	return serverName, nil
}

func (p *HTTPProxy) registerConnection(conn net.Conn) bool {
	if p == nil || conn == nil {
		return false
	}
	p.connMu.Lock()
	defer p.connMu.Unlock()
	if p.closing {
		return false
	}
	if p.conns == nil {
		p.conns = make(map[net.Conn]struct{})
	}
	p.conns[conn] = struct{}{}
	p.connWG.Add(1)
	return true
}

func (p *HTTPProxy) unregisterConnection(conn net.Conn) {
	if p == nil || conn == nil {
		return
	}
	p.connMu.Lock()
	defer p.connMu.Unlock()
	delete(p.conns, conn)
}

func (p *HTTPProxy) beginClose() []net.Conn {
	if p == nil {
		return nil
	}
	p.connMu.Lock()
	p.closing = true
	connections := make([]net.Conn, 0, len(p.conns))
	for conn := range p.conns {
		connections = append(connections, conn)
	}
	p.connMu.Unlock()
	return connections
}

func (p *HTTPProxy) context() context.Context {
	if p == nil || p.ctx == nil {
		return context.Background()
	}
	return p.ctx
}

func (p *HTTPProxy) dialContext(ctx context.Context, _, address string) (net.Conn, error) {
	host, port, err := p.authorizeTarget(address, "80", "80")
	if err != nil {
		return nil, err
	}
	return p.dialResolved(ctx, host, port)
}

func (p *HTTPProxy) authorizeTarget(raw, defaultPort, requiredPort string) (string, string, error) {
	host, port, err := splitProxyTarget(raw, defaultPort)
	if err != nil {
		return "", "", err
	}
	if port != requiredPort {
		return "", "", fmt.Errorf("sandbox proxy only permits port %s", requiredPort)
	}
	if net.ParseIP(host) != nil {
		return "", "", errors.New("sandbox proxy does not permit IP targets")
	}
	host, err = normalizeHost(host)
	if err != nil {
		return "", "", err
	}
	if _, ok := p.allowed[host]; !ok {
		return "", "", fmt.Errorf("sandbox proxy host %q is not allowlisted", host)
	}
	return host, port, nil
}

func splitProxyTarget(raw, defaultPort string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "@/") {
		return "", "", errors.New("invalid proxy target")
	}
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		return host, port, nil
	}
	if strings.Contains(raw, ":") {
		return "", "", errors.New("invalid proxy target")
	}
	return raw, defaultPort, nil
}

func (p *HTTPProxy) dialResolved(ctx context.Context, host, port string) (net.Conn, error) {
	if p.dialOverride != nil {
		return p.dialOverride(ctx, host, port)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 20 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if unsafeProxyIP(ip) {
			continue
		}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("sandbox proxy target has no public address")
}

func unsafeProxyIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, prefix := range nonPublicProxyPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// These IANA special-use ranges are not covered by netip.IsPrivate but must
// never be a proxy egress target. Keep the list explicit so policy changes are
// reviewable rather than relying on resolver or routing behavior.
var nonPublicProxyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func writeProxyError(w io.Writer, status int, message string) {
	response := &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status) + " " + http.StatusText(status),
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(message + "\n")),
	}
	response.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response.Header.Set("Connection", "close")
	_ = response.Write(w)
}
