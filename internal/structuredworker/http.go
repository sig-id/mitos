package structuredworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPClient struct {
	URL, Token string
	HTTP       *http.Client
}
type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("worker HTTP %d: %s", e.Status, e.Code) }

// Call retries uncertain responses using exactly the original request bytes.
func (c *HTTPClient) Call(ctx context.Context, operation string, in, out any) error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("worker requires an HTTPS authority URL")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	delay := 100 * time.Millisecond
	for {
		req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/worker/v1/"+operation, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set(VersionHeader, Version)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := copy.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 17<<20+1))
			resp.Body.Close()
			if readErr == nil && len(body) <= 17<<20 {
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					if resp.Header.Get(VersionHeader) != Version {
						return errors.New("unsupported worker response version")
					}
					return json.Unmarshal(body, out)
				}
				if resp.StatusCode < 500 {
					var failure struct {
						Error string `json:"error"`
					}
					_ = json.Unmarshal(body, &failure)
					return &HTTPError{Status: resp.StatusCode, Code: failure.Error}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}
