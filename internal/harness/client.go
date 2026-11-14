package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Delivery struct {
	RequestID string          `json:"request_id"`
	State     string          `json:"state"`
	Digest    string          `json:"digest"`
	Body      json.RawMessage `json:"body"`
	RunIDs    []string        `json:"run_ids"`
}

// MaxObservationBytes is the inclusive JSON payload limit shared by the native
// readers and observation endpoint. Line terminators do not count toward it.
// Approval requests retain their smaller independent limit.
const MaxObservationBytes = 1 << 20

type Observer interface {
	Observe(context.Context, json.RawMessage) error
}

func ObservationRelevant(raw json.RawMessage) bool {
	var e struct {
		Method string `json:"method"`
		Params struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &e) != nil || (e.Method != "item/started" && e.Method != "item/completed") {
		return false
	}
	switch e.Params.Item.Type {
	case "commandExecution", "fileChange", "mcpToolCall":
		return true
	}
	return false
}
func (c *Client) Observe(ctx context.Context, raw json.RawMessage) error {
	copy := *c
	httpClient := *c.HTTP
	httpClient.Timeout = 125 * time.Second
	copy.HTTP = &httpClient
	// Preserve native bytes. Re-marshaling RawMessage HTML-escapes output and
	// can grow an in-limit observation beyond the server's body limit.
	return copy.callJSON(ctx, "POST", c.base()+"/observe", raw, nil)
}

type Broker interface {
	Submit(context.Context, json.RawMessage) (Delivery, error)
	Get(context.Context, string) (Delivery, error)
	Deliver(context.Context, string, string, string) error
	Invalidate(context.Context, string) error
}
type Client struct {
	ProjectID, DeskSessionID string
	URL, SessionID, Token    string
	HTTP                     *http.Client
}

func NewClient(base, id, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return nil, errors.New("agent bridge requires the local Studio URL")
	}
	return &Client{URL: strings.TrimRight(base, "/"), SessionID: id, Token: token, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Mitos agent endpoint returned %d", e.Status) }
func (c *Client) call(ctx context.Context, method, path string, in any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.callJSON(ctx, method, path, b, out)
}

func (c *Client) callJSON(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{res.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 32<<10))
		return err
	}
	return json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(out)
}
func (c *Client) Start(ctx context.Context, adapter, title, workspace string) error {
	var out struct {
		Session struct {
			ID string `json:"session_id"`
		} `json:"session"`
		Token string `json:"token"`
	}
	if err := c.call(ctx, "POST", "/api/agent-sessions", map[string]string{"adapter": adapter, "title": title, "workspace": workspace, "project_id": c.ProjectID, "desk_session_id": c.DeskSessionID}, &out); err != nil {
		return err
	}
	c.SessionID, c.Token = out.Session.ID, out.Token
	return nil
}
func (c *Client) base() string { return "/api/agent-sessions/" + url.PathEscape(c.SessionID) }
func (c *Client) Heartbeat(ctx context.Context) error {
	return c.call(ctx, "POST", c.base()+"/heartbeat", nil, nil)
}
func (c *Client) Close(ctx context.Context) error {
	return c.call(ctx, "POST", c.base()+"/close", nil, nil)
}
func (c *Client) Submit(ctx context.Context, raw json.RawMessage) (Delivery, error) {
	var d Delivery
	err := c.call(ctx, "POST", c.base()+"/requests", raw, &d)
	return d, err
}
func (c *Client) Get(ctx context.Context, key string) (Delivery, error) {
	var d Delivery
	err := c.call(ctx, "GET", c.base()+"/requests/"+url.PathEscape(key), nil, &d)
	return d, err
}
func (c *Client) Deliver(ctx context.Context, key, digest, state string) error {
	return c.call(ctx, "POST", c.base()+"/requests/"+url.PathEscape(key)+"/delivery", map[string]string{"digest": digest, "state": state}, nil)
}
func (c *Client) Invalidate(ctx context.Context, key string) error {
	return c.call(ctx, "POST", c.base()+"/requests/"+url.PathEscape(key)+"/invalidate", nil, nil)
}
func Wait(ctx context.Context, b Broker, key string) (Delivery, error) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		d, err := b.Get(ctx, key)
		if err == nil && d.State != "pending" {
			return d, nil
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.Status >= 400 && httpErr.Status < 500 {
			return Delivery{}, err
		}
		select {
		case <-ctx.Done():
			return Delivery{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func (c *Client) EndThread(ctx context.Context, thread string) error {
	return c.call(ctx, "POST", c.base()+"/end-thread", map[string]string{"thread": thread}, nil)
}
