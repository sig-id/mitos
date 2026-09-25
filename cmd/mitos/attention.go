package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mitos/internal/desk"
	"mitos/internal/harness"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func attentionHook() error { return attentionHookIO(os.Stdin, os.Stdout) }

func attentionHookIO(input io.Reader, outputWriter io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("invalid attention hook input")
	}
	var event struct {
		Name  string `json:"hook_event_name"`
		Tool  string `json:"tool_name"`
		Input struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return errors.New("invalid attention hook JSON")
	}
	client, err := harness.NewClient(os.Getenv("MITOS_URL"), "", "")
	if err != nil {
		return err
	}
	session := os.Getenv("MITOS_SESSION_ID")
	if session == "" {
		return errors.New("attention hook requires a Mitos session")
	}
	httpClient := &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := client.URL + "/api/sessions/" + url.PathEscape(session) + "/inbox"
	res, err := httpClient.Get(base)
	if err != nil {
		return errors.New("Mitos attention service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("Mitos attention service unavailable")
	}
	var notices []desk.Notice
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&notices) != nil {
		return errors.New("invalid attention response")
	}
	var context strings.Builder
	held := false
	var delivered []desk.Notice
	for _, n := range notices {
		if n.Pause && n.ReleasedAt == nil {
			held = true
		}
		for _, target := range n.Targets {
			if target.SessionID != session || target.AcknowledgedAt != nil && n.ReleasedAt == nil {
				continue
			}
			fmt.Fprintf(&context, "Mitos attention %s from %s: %s\n", n.ID, n.Author, n.Body)
			if n.ReleasedAt != nil {
				fmt.Fprintln(&context, "RELEASED: recheck other breakpoints, then continue.")
			} else if n.Pause {
				fmt.Fprintln(&context, "BREAKPOINT: stop new work; acknowledge when safe and wait for release.")
			}
			fmt.Fprintf(&context, "Use \"$MITOS_BIN\" desk acknowledge %s. Use desk inbox to check release. Project rules still apply.\n", n.ID)
			delivered = append(delivered, n)
		}
	}
	output := map[string]any{"hookEventName": event.Name}
	if context.Len() > 0 {
		output["additionalContext"] = context.String()
	}
	control := regexp.MustCompile(`^(?:"\$MITOS_BIN"|mitos) desk (?:inbox(?: --wait)?|acknowledge [A-Za-z0-9_-]+|release-notice [A-Za-z0-9_-]+)$`).MatchString(event.Input.Command)
	if event.Name == "PreToolUse" && held && !(event.Tool == "Bash" && control) {
		output["permissionDecision"] = "deny"
		output["permissionDecisionReason"] = "Mitos breakpoint is active. Acknowledge when safe using the Mitos inbox and wait for release. Already-running commands are not cancelled."
	}
	if err := json.NewEncoder(outputWriter).Encode(map[string]any{"hookSpecificOutput": output}); err != nil {
		return err
	}
	for _, n := range delivered {
		payload, _ := json.Marshal(map[string]bool{"release": n.ReleasedAt != nil})
		r, err := httpClient.Post(base+"/"+url.PathEscape(n.ID)+"/delivered", "application/json", bytes.NewReader(payload))
		if err == nil {
			r.Body.Close()
		}
	}
	return nil
}
