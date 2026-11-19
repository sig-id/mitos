package compute

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Brokered model access (ADR 0008).
//
// An agent harness under Nucleus holds no credential. It speaks plain HTTP to
// a loopback base URL inside its container; Nucleus relays each connection to
// this broker's per-attempt unix socket, which is never mounted. The broker
// forwards only to the vendor API named by the first path segment, removes
// whatever credential the client sent and adds the operator's key. TLS to the
// vendor starts here on the host, so the sandbox needs no CA bundle and never
// holds a key that a command it runs could read and replay.

// BrokerPlaceholderKey is the credential a brokered harness is configured
// with. It only satisfies the harness's own startup checks; the broker
// replaces it on every request.
const BrokerPlaceholderKey = "mitos-brokered"

// BrokerVendorAnthropic and BrokerVendorOpenAI name the vendor APIs the
// broker serves; each is the first path segment of its base URL.
const (
	BrokerVendorAnthropic = "anthropic"
	BrokerVendorOpenAI    = "openai"
)

type modelVendor struct {
	upstream   string
	credential func(http.Header, string)
}

var modelVendors = map[string]modelVendor{
	BrokerVendorAnthropic: {"https://api.anthropic.com", func(h http.Header, key string) { h.Set("X-Api-Key", key) }},
	BrokerVendorOpenAI:    {"https://api.openai.com", func(h http.Header, key string) { h.Set("Authorization", "Bearer "+key) }},
}

// clientCredentialHeaders are removed from every brokered request: the only
// credential that reaches a vendor is the one the broker adds.
var clientCredentialHeaders = []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie", "Chatgpt-Account-Id", "Openai-Organization", "Openai-Project"}

// BrokerBaseURL is the base URL a brokered workload uses for a vendor API.
func BrokerBaseURL(vendor string) string { return "http://" + EgressLoopAddr + "/" + vendor }

// ModelBroker serves one attempt's brokered model access on a unix socket.
type ModelBroker struct {
	path     string
	listener net.Listener
	server   *http.Server
	routes   map[string]*httputil.ReverseProxy
}

// StartModelBroker serves the vendors in keys (vendor name to API key) on a
// new unix socket at path, readable only by the server's user.
func StartModelBroker(path string, keys map[string]string) (*ModelBroker, error) {
	return startModelBroker(path, keys, nil, nil)
}

// startModelBroker lets tests replace vendor upstreams and the transport.
func startModelBroker(path string, keys map[string]string, upstreams map[string]string, transport http.RoundTripper) (*ModelBroker, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("model broker needs an absolute socket path")
	}
	// sockaddr_un holds 108 bytes including the terminating NUL.
	if len(path) > 107 {
		return nil, fmt.Errorf("model broker socket path is too long for a unix socket: %s", path)
	}
	if len(keys) == 0 {
		return nil, errors.New("model broker needs at least one vendor credential")
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	b := &ModelBroker{path: path, routes: map[string]*httputil.ReverseProxy{}}
	vendors := make([]string, 0, len(keys))
	for vendor := range keys {
		vendors = append(vendors, vendor)
	}
	sort.Strings(vendors)
	for _, vendor := range vendors {
		spec, ok := modelVendors[vendor]
		key := keys[vendor]
		if !ok {
			return nil, fmt.Errorf("model broker does not serve vendor %q", vendor)
		}
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("model broker credential for %s is empty or malformed", vendor)
		}
		upstream := spec.upstream
		if override, ok := upstreams[vendor]; ok {
			upstream = override
		}
		target, err := url.Parse(upstream)
		if err != nil {
			return nil, err
		}
		b.routes[vendor] = vendorProxy(vendor, target, key, spec.credential, transport)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	b.listener = listener
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

func vendorProxy(vendor string, target *url.URL, key string, credential func(http.Header, string), transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// The client may use origin-form or, through proxy variables,
			// absolute-form; either way only the path after the vendor
			// segment and the query survive. Scheme and host are fixed.
			escaped := strings.TrimPrefix(pr.In.URL.EscapedPath(), "/"+vendor)
			out := *target
			out.Path = strings.TrimSuffix(target.Path, "/") + mustUnescape(escaped)
			out.RawPath = ""
			if escaped != mustUnescape(escaped) {
				out.RawPath = strings.TrimSuffix(target.EscapedPath(), "/") + escaped
			}
			out.RawQuery = pr.In.URL.RawQuery
			pr.Out.URL = &out
			pr.Out.Host = ""
			for _, name := range clientCredentialHeaders {
				pr.Out.Header.Del(name)
			}
			credential(pr.Out.Header, key)
		},
		// Model responses stream; deliver every chunk as it arrives.
		FlushInterval: -1,
		Transport:     transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "mitos broker: "+vendor+" is unreachable", http.StatusBadGateway)
		},
	}
}

func mustUnescape(escaped string) string {
	if path, err := url.PathUnescape(escaped); err == nil {
		return path
	}
	return escaped
}

func (b *ModelBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		http.Error(w, "mitos broker: tunnels are not brokered; use the model base URL", http.StatusForbidden)
		return
	}
	vendor, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	proxy, ok := b.routes[vendor]
	if !ok || rest == "" {
		http.Error(w, "mitos broker: no brokered model API at this path", http.StatusForbidden)
		return
	}
	proxy.ServeHTTP(w, r)
}

// Endpoint is the broker's address in launch-spec form.
func (b *ModelBroker) Endpoint() string { return "unix://" + b.path }

// Close stops serving, ends open exchanges and removes the socket.
func (b *ModelBroker) Close() error {
	err := b.server.Close()
	if removeErr := os.Remove(b.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		err = errors.Join(err, removeErr)
	}
	return err
}
