package compute

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRequest struct {
	method, uri string
	header      http.Header
}

func recordingUpstream(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, recordedRequest{r.Method, r.RequestURI, r.Header.Clone()})
		mu.Unlock()
		if r.URL.Path == "/v1/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	return server, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), seen...)
	}
}

func testBroker(t *testing.T, upstream string) *ModelBroker {
	t.Helper()
	path := filepath.Join(t.TempDir(), "b.sock")
	broker, err := startModelBroker(path, map[string]string{BrokerVendorAnthropic: "sk-ant-real", BrokerVendorOpenAI: "sk-oai-real"}, map[string]string{BrokerVendorAnthropic: upstream, BrokerVendorOpenAI: upstream + "/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { broker.Close() })
	return broker
}

func brokerClient(b *ModelBroker) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", b.path)
	}}}
}

func TestModelBrokerReplacesClientCredentials(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	broker := testBroker(t, upstream.URL)
	client := brokerClient(broker)
	for _, tc := range []struct{ path, want string }{
		{"/anthropic/v1/messages?beta=true", "/v1/messages?beta=true"},
		{"/openai/v1/responses", "/v1/responses"},
	} {
		req, _ := http.NewRequest("POST", "http://"+EgressLoopAddr+tc.path, strings.NewReader("{}"))
		req.Header.Set("X-Api-Key", BrokerPlaceholderKey)
		req.Header.Set("Authorization", "Bearer "+BrokerPlaceholderKey)
		req.Header.Set("Cookie", "session=stolen")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", tc.path, resp.StatusCode)
		}
		got := seen()[len(seen())-1]
		if got.uri != tc.want {
			t.Fatalf("forwarded %s as %s", tc.path, got.uri)
		}
		if got.header.Get("Cookie") != "" {
			t.Fatalf("%s: client cookie forwarded", tc.path)
		}
		if strings.HasPrefix(tc.path, "/anthropic") {
			if got.header.Get("X-Api-Key") != "sk-ant-real" || got.header.Get("Authorization") != "" {
				t.Fatalf("anthropic credentials %v", got.header)
			}
		} else if got.header.Get("Authorization") != "Bearer sk-oai-real" || got.header.Get("X-Api-Key") != "" {
			t.Fatalf("openai credentials %v", got.header)
		}
	}
}

// Through injected proxy variables a client sends absolute-form requests; the
// broker still decides the destination from the path alone.
func TestModelBrokerAcceptsAbsoluteFormAndRefusesTunnels(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	broker := testBroker(t, upstream.URL)
	exchange := func(request string) string {
		conn, err := net.Dial("unix", broker.path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		io.WriteString(conn, request)
		status, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := exchange("GET http://evil.example/openai/v1/models HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n"); !strings.Contains(status, " 200 ") {
		t.Fatalf("absolute-form: %s", status)
	}
	if got := seen(); len(got) != 1 || got[0].uri != "/v1/models" {
		t.Fatalf("absolute-form forwarded as %+v", got)
	}
	for _, request := range []string{
		"CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n",
		"GET /google/v1/models HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
		"GET /openai HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n",
	} {
		if status := exchange(request); !strings.Contains(status, " 403 ") {
			t.Fatalf("%q answered %s", request, status)
		}
	}
	if len(seen()) != 1 {
		t.Fatal("a refused request reached the upstream")
	}
}

func TestModelBrokerStreamsResponses(t *testing.T) {
	upstream, _ := recordingUpstream(t)
	broker := testBroker(t, upstream.URL)
	resp, err := brokerClient(broker).Get("http://" + EgressLoopAddr + "/openai/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- text
	}()
	select {
	case text := <-line:
		if text != "data: first\n" {
			t.Fatalf("first event %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streamed event was buffered")
	}
}

func TestModelBrokerRefusesUnusableConfiguration(t *testing.T) {
	dir := t.TempDir()
	for name, keys := range map[string]map[string]string{
		"none":      {},
		"vendor":    {"google": "k"},
		"empty key": {BrokerVendorOpenAI: ""},
		"header":    {BrokerVendorOpenAI: "k\r\nX-Evil: 1"},
	} {
		if _, err := StartModelBroker(filepath.Join(dir, name+".sock"), keys); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := StartModelBroker("relative.sock", map[string]string{BrokerVendorOpenAI: "k"}); err == nil {
		t.Fatal("relative socket accepted")
	}
	broker, err := StartModelBroker(filepath.Join(dir, "ok.sock"), map[string]string{BrokerVendorOpenAI: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if broker.Endpoint() != "unix://"+filepath.Join(dir, "ok.sock") {
		t.Fatalf("endpoint %s", broker.Endpoint())
	}
	broker.Close()
	if _, err := net.Dial("unix", filepath.Join(dir, "ok.sock")); err == nil {
		t.Fatal("closed broker still accepts connections")
	}
}
