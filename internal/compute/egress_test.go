package compute

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary act as the in-sandbox egress helper: the
// confined argv wrapper starts "<helper> egress-loop ..." in the background.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "egress-loop" {
		socket, listen := "", EgressLoopAddr
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--socket":
				i++
				socket = args[i]
			case "--listen":
				i++
				listen = args[i]
			}
		}
		if socket == "" {
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := RunEgressLoop(ctx, socket, listen); err != nil {
			fmt.Fprintln(os.Stderr, "egress-loop:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// unixSocketDir returns a temp directory whose paths fit every supported
// platform's unix socket address limit: macOS allows 104 bytes of sun_path
// and the standard test temp root is longer on some machines.
func unixSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mitos-egress-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func readStatus(t *testing.T, conn net.Conn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
}

// The relay admits only allowlisted model endpoints, and only over https.
func TestEgressRelayEnforcesAllowlist(t *testing.T) {
	path := filepath.Join(unixSocketDir(t), "egress", "model.sock")
	dialed := ""
	upstream, server := net.Pipe()
	relay, err := startEgressRelay(path, []string{"api.openai.com"}, func(addr string) (net.Conn, error) {
		dialed = addr
		return upstream, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	connect := func(request string) net.Conn {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Write([]byte(request)); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	// Allowed host: tunnel established, bytes flow both directions.
	allowed := connect("CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n")
	defer allowed.Close()
	if status := readStatus(t, allowed); status != "HTTP/1.1 200 Connection Established" {
		t.Fatal("allowed host was not tunnelled:", status)
	}
	if dialed != "api.openai.com:443" {
		t.Fatal("relay dialled the wrong target:", dialed)
	}
	go allowed.Write([]byte("request"))
	go server.Read(make([]byte, 16))
	if _, err = server.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if err = allowed.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if _, err = allowed.Read(buffer); err != nil || string(buffer) != "response" {
		t.Fatal("tunnel did not carry model traffic", err, string(buffer))
	}

	// Same-registration subdomains stay reachable; one level only.
	subdomain := connect("CONNECT eu.api.openai.com:443 HTTP/1.1\r\nHost: eu.api.openai.com:443\r\n\r\n")
	defer subdomain.Close()
	if status := readStatus(t, subdomain); status != "HTTP/1.1 200 Connection Established" {
		t.Fatal("regional endpoint refused:", status)
	}

	// The loopback operator listener is not on the allowlist.
	denied := connect("CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n")
	defer denied.Close()
	if status := readStatus(t, denied); status != "HTTP/1.1 403 Forbidden" {
		t.Fatal("host listener was reachable through the relay:", status)
	}

	// Plain HTTP or non-model ports are not mediated at all.
	plain := connect("CONNECT api.openai.com:8080 HTTP/1.1\r\nHost: api.openai.com:8080\r\n\r\n")
	defer plain.Close()
	if err = plain.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = plain.Read(make([]byte, 16)); err == nil {
		t.Fatal("non-https port was mediated")
	}
	_ = relay

	// A non-CONNECT request is refused, not dialled.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Write([]byte("GET http://api.openai.com/v1 HTTP/1.1\r\nHost: api.openai.com\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err = other.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Read(make([]byte, 16)); err == nil {
		t.Fatal("plain proxying was mediated")
	}
}

func TestEgressRelayReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(unixSocketDir(t), "model.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	relay, err := StartEgressRelay(path, DefaultModelHosts)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// The loop forwarder forwards CONNECT heads verbatim to the relay socket.
func TestEgressLoopForwardsTunnelsToRelaySocket(t *testing.T) {
	relayPath := filepath.Join(unixSocketDir(t), "relay.sock")
	received := make(chan string, 1)
	listener, err := net.Listen("unix", relayPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		head := make([]byte, 128)
		n, _ := conn.Read(head)
		received <- string(head[:n])
	}()
	// The loop's loopback bring-up requires a private namespace; test the
	// forwarding half directly with the listener bound to a spare port.
	loop, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()
	go func() {
		conn, err := loop.Accept()
		if err != nil {
			return
		}
		forwardTunnel(conn, relayPath)
	}()
	conn, err := net.Dial("tcp", loop.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case head := <-received:
		if !strings.HasPrefix(head, "CONNECT api.openai.com:443 ") {
			t.Fatal("forwarded head lost its target:", head)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel head was never forwarded")
	}
}

// TestEgressMediationProbe runs inside a mediated sandbox: the argv wrapper has
// already started the egress loop, so this probe only needs to tunnel one
// request through it to the stand-in model endpoint.
func TestEgressMediationProbe(t *testing.T) {
	if len(flag.Args()) != 1 {
		return
	}
	upstream := flag.Args()[0]
	var conn net.Conn
	var err error
	for range 50 {
		conn, err = net.Dial("tcp", EgressLoopAddr)
		if err == nil {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if err != nil {
		fmt.Println("no loop:", err)
		entries, _ := os.ReadDir("/proc")
		for _, entry := range entries {
			raw, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
			if readErr == nil && strings.Contains(string(raw), "egress-loop") {
				fmt.Printf("helper alive: pid %s cmd %q\n", entry.Name(), string(raw))
			}
		}
		return
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("CONNECT " + upstream + " HTTP/1.1\r\nHost: " + upstream + "\r\n\r\n")); err != nil {
		fmt.Println("write:", err)
		return
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, "200") {
		fmt.Println("status:", strings.TrimSpace(status), err)
		return
	}
	if _, err = conn.Write([]byte("model-request")); err != nil {
		fmt.Println("send:", err)
		return
	}
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		fmt.Println("deadline:", err)
		return
	}
	reply := make([]byte, 64)
	n, err := conn.Read(reply)
	if err != nil || string(reply[:n]) != "model-ok" {
		fmt.Printf("reply: %q %v\n", string(reply[:n]), err)
		return
	}
	fmt.Println("tunnelled")
	os.Exit(0)
}

// A mediated sandbox must keep host listeners unreachable — including the
// loopback operator API — while the allowlisted model transport still works.
func TestConfinedMediatedEgressIsolatesHostAndMediatesModel(t *testing.T) {
	if _, err := exec.LookPath(ConfinementBwrap); err != nil {
		t.Skip("bwrap required for mediated egress tests")
	}
	// Stand-in model endpoint: accept one tunnelled request and answer.
	upstreamListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstreamListener.Close()
	go func() {
		conn, err := upstreamListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Read(make([]byte, 64))
		_, _ = conn.Write([]byte("model-ok"))
	}()
	// A live host socket that must stay unreachable from the sandbox.
	hostListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostListener.Close()

	socket := filepath.Join(t.TempDir(), "egress", "model.sock")
	relay, err := startEgressRelay(socket, []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	relay.ports = func(string) bool { return true } // stand-in endpoint port

	l := &Local{Confinement: Confinement{Tool: ConfinementBwrap, NetMode: NetUnrestricted, EgressSocket: socket, HelperExecutable: os.Args[0]}}
	l.SetEgressHelper(os.Args[0])
	root := t.TempDir()
	base := filepath.Join(root, "attempt")
	workspace := filepath.Join(base, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	spec := confiningLaunch(t, l, base, workspace)
	spec.Posture.NetMode = NetUnrestricted
	spec.Posture.BrokerEndpoint = "unix://" + socket

	out, runErr := runConfined(t, l, spec, []string{os.Args[0], "-test.run=^TestEgressMediationProbe$", "--", upstreamListener.Addr().String()})
	if runErr != nil || strings.TrimSpace(out) != "tunnelled" {
		t.Fatalf("mediated model transport failed: %q %v", out, runErr)
	}
	out, runErr = runConfined(t, l, spec, []string{os.Args[0], "-test.run=^TestConfinementNetworkProbe$", "--", hostListener.Addr().String()})
	if runErr != nil || strings.TrimSpace(out) != "blocked" {
		t.Fatalf("host listener reachable through mediated sandbox: %q %v", out, runErr)
	}
}
