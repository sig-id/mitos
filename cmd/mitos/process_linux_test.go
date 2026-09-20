package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"mitos/internal/studio"
)

func TestMain(m *testing.M) {
	if os.Getenv("MITOS_TEST_SERVER") == "1" {
		if err := run(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

type testServerProcess struct {
	workspace string
	cmd       *exec.Cmd
	done      chan struct{}
	err       error
	output    strings.Builder
}

func (p *testServerProcess) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		if p.err != nil {
			t.Fatalf("server exit: %v\n%s", p.err, p.output.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server shutdown timed out")
	}
}

func startTestServerProcess(t *testing.T, data, addr string) *testServerProcess {
	t.Helper()
	p := &testServerProcess{done: make(chan struct{}), workspace: t.TempDir()}
	p.cmd = exec.Command(os.Args[0], "serve", "--addr", addr, "--data", data, "--workspace-root", p.workspace)
	p.cmd.Env = append(os.Environ(), "MITOS_TEST_SERVER=1")
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			p.output.WriteString(line + "\n")
			if strings.Contains(line, "Mitos Studio →") {
				ready <- struct{}{}
			}
		}
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(20 * time.Second):
			p.cmd.Process.Kill()
			<-p.done
			t.Error("server cleanup timed out")
		}
	})
	select {
	case <-ready:
	case <-p.done:
		t.Fatalf("server exited before readiness: %v\n%s", p.err, p.output.String())
	case <-time.After(20 * time.Second):
		t.Fatal("server startup timed out")
	}
	return p
}

func freeTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func TestServeReplacesProcessAndStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	addr := freeTestAddress(t)
	first := startTestServerProcess(t, data, addr)
	other := startTestServerProcess(t, filepath.Join(dir, "other"), freeTestAddress(t))
	marker := filepath.Join(data, "retained.txt")
	if err := os.WriteFile(marker, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	second := startTestServerProcess(t, data, addr)
	first.wait(t)
	if first.cmd.Process.Pid == second.cmd.Process.Pid {
		t.Fatal("serve reused the previous process")
	}
	select {
	case <-other.done:
		t.Fatal("restart stopped a different data directory")
	default:
	}
	for range 2 {
		if err := run([]string{"stop", "--data", data}); err != nil {
			t.Fatal(err)
		}
	}
	second.wait(t)
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "keep this" {
		t.Fatalf("saved data changed: %q, %v", contents, err)
	}
	third := startTestServerProcess(t, data, addr)
	if err := run([]string{"stop", "--data", data}); err != nil {
		t.Fatal(err)
	}
	third.wait(t)
}

func TestStopAbsentAndUnrelatedLock(t *testing.T) {
	data := filepath.Join(t.TempDir(), "absent")
	if err := stopServer([]string{"--data", data}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("stop created data for an absent server")
	}
	store, err := studio.OpenStore(data)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := stopServer([]string{"--data", data}); err == nil {
		t.Fatal("unrelated lock accepted as a Studio process")
	}
}

func TestStopWithOpenTerminalStream(t *testing.T) {
	data, addr := t.TempDir(), freeTestAddress(t)
	server := startTestServerProcess(t, data, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	post := func(path string, body any) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+addr+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
		var result map[string]any
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	project := post("/api/projects", map[string]string{"name": "Stream", "directory": server.workspace})
	session := post("/api/projects/"+project["id"].(string)+"/sessions", map[string]string{"agent_id": "shell"})
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/api/sessions/"+session["id"].(string)+"/stream?offset=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	started := time.Now()
	if err := run([]string{"stop", "--data", data}); err != nil {
		t.Fatal(err)
	}
	server.wait(t)
	if time.Since(started) > 3*time.Second {
		t.Fatal("open stream delayed server shutdown")
	}
}
