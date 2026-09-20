package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCodexTerminalPreservesEarlyNotifications(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		scan := bufio.NewScanner(server)
		for scan.Scan() {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scan.Bytes(), &request)
			switch request.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"thread"},"model":"test"}}`)
			case "turn/start":
				// A quick server can emit notifications before the RPC response.
				// Server requests have a separate ID namespace from client RPCs.
				fmt.Fprintln(server, `{"id":3,"method":"unsupported/nativeRequest","params":{}}`)
				fmt.Fprintln(server, `{"method":"item/agentMessage/delta","params":{"delta":"preserved"}}`)
				fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn"}}}`)
				fmt.Fprintln(server, `{"id":3,"result":{}}`)
			}
		}
	}()
	var output bytes.Buffer
	if err := RunCodexTerminal(ctx, nil, client, strings.NewReader("{}\n"), &output, t.TempDir(), "test", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "preserved") {
		t.Fatal(output.String())
	}
	if !strings.Contains(output.String(), "unsupported/nativeRequest") {
		t.Fatal("native fallback request lost", output.String())
	}
}
