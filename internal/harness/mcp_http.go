package harness

// The MCP streamable HTTP transport (MCP 2025-03-26 and later): one JSON-RPC
// message per POST answered as application/json, notifications acknowledged
// with 202, no server-initiated stream, and per-attempt sessions under a
// per-attempt bearer token.
import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	mcpSessionHeader = "Mcp-Session-Id"
	mcpVersionHeader = "MCP-Protocol-Version"
	// mcpBodyLimit bounds one JSON-RPC message; tool arguments have their own
	// smaller limit inside the handler.
	mcpBodyLimit = 2 << 20
)

// NewMCPToken mints the per-attempt bearer token. It separates attempts; it
// is not a secret from the agent, which is told the endpoint and the token.
func NewMCPToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// mcpTransport serves one attempt's MCP sessions over streamable HTTP.
type mcpTransport struct {
	server *MCPServer
	token  string

	mu       sync.Mutex
	sessions map[string]bool
}

// StreamableHTTP serves the attempt's tools over the streamable HTTP
// transport. The token is checked in constant time on every request.
func (s *MCPServer) StreamableHTTP(token string) http.Handler {
	return &mcpTransport{server: s, token: token, sessions: map[string]bool{}}
}

func (t *mcpTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !t.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "mitos mcp: invalid attempt credential", http.StatusUnauthorized)
		return
	}
	if version := r.Header.Get(mcpVersionHeader); version != "" && !mcpVersionSupported(version) {
		http.Error(w, "mitos mcp: unsupported protocol version "+version, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// No server-initiated stream: tools and results all travel in
		// POST responses.
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "mitos mcp: server-initiated streams are not served", http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		if !t.endSession(r.Header.Get(mcpSessionHeader)) {
			http.Error(w, "mitos mcp: unknown session", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "mitos mcp: method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if media := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(media), "application/json") {
		http.Error(w, "mitos mcp: POST needs application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpBodyLimit+1))
	if err != nil || len(body) > mcpBodyLimit {
		http.Error(w, "mitos mcp: unreadable request body", http.StatusBadRequest)
		return
	}
	if !json.Valid(body) || len(body) == 0 || body[0] != '{' {
		// One JSON-RPC object per request; batching is not served.
		http.Error(w, "mitos mcp: expected one JSON-RPC object", http.StatusBadRequest)
		return
	}
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &probe)
	initialize := probe.Method == "initialize"
	if !initialize && !t.hasSession(r.Header.Get(mcpSessionHeader)) {
		http.Error(w, "mitos mcp: missing or unknown session", http.StatusNotFound)
		return
	}
	answer := t.server.message(r.Context(), body)
	if answer.notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if initialize && answer.err == nil {
		w.Header().Set(mcpSessionHeader, t.beginSession())
	}
	if version := negotiatedMCPVersion(answer); version != "" {
		w.Header().Set(mcpVersionHeader, version)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(answer.response())
}

func (t *mcpTransport) authorized(r *http.Request) bool {
	scheme, credential, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(credential), []byte(t.token)) == 1
}

func (t *mcpTransport) beginSession() string {
	id := NewMCPToken()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[id] = true
	return id
}

func (t *mcpTransport) hasSession(id string) bool {
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[id]
}

func (t *mcpTransport) endSession(id string) bool {
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.sessions[id] {
		return false
	}
	delete(t.sessions, id)
	return true
}

func mcpVersionSupported(version string) bool {
	for _, supported := range MCPProtocolVersions {
		if version == supported {
			return true
		}
	}
	return false
}

// negotiatedMCPVersion reads the version the handler agreed to serve, so the
// response header echoes a supported version.
func negotiatedMCPVersion(answer mcpMessage) string {
	if answer.err != nil {
		return ""
	}
	result, ok := answer.result.(map[string]any)
	if !ok {
		return ""
	}
	version, _ := result["protocolVersion"].(string)
	if !mcpVersionSupported(version) {
		return ""
	}
	return version
}
