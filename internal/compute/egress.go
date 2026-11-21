package compute

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mediated model transport for network-isolated workers.
//
// A confined worker never shares the host network namespace: it runs in its own
// namespace whose only route to the outside is the host-side egress relay,
// reached over a mounted UNIX socket. The relay speaks HTTP CONNECT and admits
// only the model endpoints on its allowlist, so the worker's HTTPS egress is
// restricted to model transport while every host service — including the
// loopback operator listener — stays unreachable.

// EgressLoopAddr is the loopback address the in-sandbox forwarder listens on.
// Proxies are wired through HTTPS_PROXY/HTTP_PROXY; the runtime's own TLS still
// terminates end to end at the model endpoint.
const EgressLoopAddr = "127.0.0.1:3128"

// DefaultModelHosts is the allowlist served by the host-side relay: the model
// API endpoints the bundled runtimes need, and nothing else.
var DefaultModelHosts = []string{"api.openai.com", "api.anthropic.com"}

// StartEgressRelay serves the allowlisted CONNECT relay on a UNIX socket. Each
// connection carries exactly one CONNECT tunnel; denied or undialable targets
// are answered and closed, never dialed.
func StartEgressRelay(path string, allowed []string) (*EgressRelay, error) {
	return startEgressRelay(path, allowed, nil)
}

// maxSocketPath is the smallest sun_path size among supported hosts (macOS,
// 104 bytes including the terminator; Linux allows 108).
const maxSocketPath = 104

func startEgressRelay(path string, allowed []string, dial egressDialer) (*EgressRelay, error) {
	if !filepath.IsAbs(path) || len(allowed) == 0 {
		return nil, errors.New("egress relay needs an absolute socket path and allowlist")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// A stale socket from a previous run is replaced, never reused.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		if len(path) >= maxSocketPath {
			// The kernel's bind error (EINVAL) does not name the cause.
			return nil, fmt.Errorf("egress socket path is %d bytes, beyond the %d-byte Unix socket limit; use a shorter data directory: %w", len(path), maxSocketPath-1, err)
		}
		return nil, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	relay := &EgressRelay{listener: listener, allowed: append([]string(nil), allowed...), dial: dial, ports: func(port string) bool { return port == "443" }}
	go relay.serve()
	return relay, nil
}

type EgressRelay struct {
	listener net.Listener
	allowed  []string
	// ports decides which target ports may be mediated; production mediates
	// https only. Tests may widen it.
	ports func(port string) bool
	// dial overrides target dialing for tests; production uses a timed TCP dial.
	dial egressDialer
}

type egressDialer func(addr string) (net.Conn, error)

func (r *EgressRelay) Close() error { return r.listener.Close() }

func (r *EgressRelay) serve() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.tunnel(conn)
	}
}

func (r *EgressRelay) tunnel(conn net.Conn) {
	defer conn.Close()
	_, target, err := readConnectHead(conn, func(port string) bool { return r.ports(port) })
	if err != nil {
		return
	}
	host := hostOnly(target)
	if !r.permits(host) {
		fmt.Fprintf(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	upstream, err := r.dialUpstream(target)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	pipe(conn, upstream)
}

func (r *EgressRelay) dialUpstream(target string) (net.Conn, error) {
	if r.dial != nil {
		return r.dial(target)
	}
	return net.DialTimeout("tcp", target, 10*time.Second)
}

// permits reports whether host is on the allowlist, honouring one level of
// subdomain so regional endpoints keep working.
func (r *EgressRelay) permits(host string) bool {
	for _, allowed := range r.allowed {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func hostOnly(target string) string {
	if host, _, err := net.SplitHostPort(target); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(target)
}

// readConnectHead reads one HTTP request head and returns the raw bytes plus
// the CONNECT target. The head is forwarded verbatim to the host relay.
// permittedPort decides which target ports may be mediated at this hop; the
// host relay allows only https, the in-sandbox loop forwards everything and
// lets the relay decide.
func readConnectHead(conn net.Conn, permittedPort func(port string) bool) ([]byte, string, error) {
	reader := bufio.NewReader(conn)
	var head []byte
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, "", err
		}
		head = append(head, line...)
		if len(bytesTrim(line)) == 0 {
			break
		}
		if len(head) > 16<<10 {
			return nil, "", errors.New("proxy request head too large")
		}
	}
	request := string(head)
	first, _, _ := strings.Cut(request, "\r\n")
	fields := strings.Fields(first)
	if len(fields) != 3 || fields[0] != "CONNECT" {
		return nil, "", errors.New("only CONNECT tunnels are mediated")
	}
	target := fields[1]
	if port := portOf(target); port != "" && !permittedPort(port) {
		return nil, "", errors.New("mediated egress is restricted to permitted ports")
	}
	return head, target, nil
}

func portOf(target string) string {
	_, port, err := net.SplitHostPort(target)
	if err != nil {
		return ""
	}
	return port
}

func bytesTrim(line []byte) []byte {
	return []byte(strings.TrimRight(string(line), "\r\n"))
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copy := func(from, to net.Conn) {
		_, _ = io.Copy(to, from)
		_ = to.Close()
		done <- struct{}{}
	}
	go copy(a, b)
	go copy(b, a)
	<-done
}

// RunEgressLoop is the in-sandbox half of the mediated transport. It ensures
// the namespace's loopback interface is usable, accepts local CONNECT requests
// and forwards each tunnel over the mounted relay socket, where the allowlist
// is enforced.
func RunEgressLoop(ctx context.Context, socket, listen string) error {
	if !filepath.IsAbs(socket) {
		return errors.New("egress loop needs an absolute relay socket path")
	}
	// Bubblewrap already raises loopback in namespaces it creates, and drops
	// the capabilities netlink would need; only fall back to raising it
	// ourselves when the interface is actually unusable.
	if !loopbackReady() {
		if err := loopbackUp(); err != nil {
			return fmt.Errorf("egress loop cannot raise loopback: %w", err)
		}
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go forwardTunnel(conn, socket)
	}
}

func forwardTunnel(conn net.Conn, socket string) {
	defer conn.Close()
	head, _, err := readConnectHead(conn, func(string) bool { return true })
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	relay, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer relay.Close()
	if _, err = relay.Write(head); err != nil {
		return
	}
	pipe(conn, relay)
}

// loopbackReady reports whether loopback can actually carry a connection.
func loopbackReady() bool {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer listener.Close()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
