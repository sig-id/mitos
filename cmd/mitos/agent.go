package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mitos/internal/harness"
)

func agentClient(args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		fmt.Println("mitos agent <codex|codex-proxy|claude|claude-hook> [--url URL] [--workspace DIR] [--title TITLE] -- [harness arguments]\n\nThe Codex proxy speaks the native app-server stdio protocol. The Claude wrapper\ninstalls per-process hooks; both route supported waiting requests through Mitos.")
		return nil
	}
	if args[0] == "claude-hook" {
		return claudeHook()
	}
	adapter := args[0]
	if adapter != "codex-proxy" && adapter != "codex" && adapter != "claude" {
		return errors.New("unknown agent adapter")
	}
	flags := flag.NewFlagSet("agent "+adapter, flag.ContinueOnError)
	base := flags.String("url", "http://127.0.0.1:4646", "Local Studio URL")
	workspace := flags.String("workspace", ".", "Harness working directory")
	prompt := flags.String("prompt", "", "Initial Codex prompt; exits when the turn completes")
	executable := flags.String("executable", "", "Selected native CLI executable")
	bindDesk := flags.Bool("bind-desk-session", false, "Bind an explicitly hosted bridge to its desk session")
	keepOpen := flags.Bool("keep-open", false, "Keep the session open after the initial prompt")
	plan := flags.Bool("plan", false, "Use Codex plan mode, including structured questions")
	title := flags.String("title", "Agent request", "Title for review cards")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	dir, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	client, err := harness.NewClient(*base, "", "")
	if err != nil {
		return err
	}
	if *bindDesk {
		client.ProjectID, client.DeskSessionID = os.Getenv("MITOS_PROJECT_ID"), os.Getenv("MITOS_SESSION_ID")
	}
	name := adapter
	if name == "codex-proxy" {
		name = "codex"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err = client.Start(ctx, name, *title, dir); err != nil {
		return err
	}
	defer func() {
		end, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = client.Close(end)
	}()
	go func() {
		lastHealthy := time.Now()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := client.Heartbeat(ctx); err != nil {
					var status *harness.HTTPError
					if errors.As(err, &status) && status.Status >= 400 && status.Status < 500 {
						cancel()
						return
					}
					if time.Since(lastHealthy) > 25*time.Second {
						cancel()
						return
					}
				} else {
					lastHealthy = time.Now()
				}
			}
		}
	}()
	if adapter == "codex-proxy" || adapter == "codex" {
		argv := append([]string{"app-server"}, flags.Args()...)
		native := *executable
		if native == "" {
			native = "codex"
		}
		cmd := exec.CommandContext(ctx, native, argv...)
		cmd.Dir = dir
		cmd.Stderr = os.Stderr
		input, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err = cmd.Start(); err != nil {
			return err
		}
		stream := &processStream{Reader: output, Writer: input, close: func() error { _ = input.Close(); return output.Close() }}
		var proxyErr error
		if adapter == "codex" {
			proxyErr = harness.RunCodexTerminalOptions(ctx, client, stream, os.Stdin, os.Stdout, dir, *prompt, *plan, *keepOpen)
		} else {
			proxyErr = harness.ProxyCodex(ctx, client, stream, os.Stdin, os.Stdout)
		}
		cancel()
		_ = stream.Close()
		waitErr := cmd.Wait()
		if proxyErr != nil && !errors.Is(proxyErr, io.EOF) && !errors.Is(proxyErr, context.Canceled) {
			return proxyErr
		}
		if ctx.Err() != nil {
			return nil
		}
		return waitErr
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "' agent claude-hook"
	hooks := map[string]any{}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch", "SessionEnd"} {
		matcher := ""
		if event != "SessionEnd" && event != "PostToolBatch" {
			matcher = "Bash|Write|Edit|AskUserQuestion"
		}
		timeout := 3600
		if event == "SessionEnd" {
			timeout = 5
		}
		hooks[event] = []any{map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": timeout}}}}
	}
	settings, _ := json.Marshal(map[string]any{"hooks": hooks})
	// Pass a JSON settings value to this invocation; never rewrite user settings.
	argv := append([]string{"--settings", string(settings)}, flags.Args()...)
	native := *executable
	if native == "" {
		native = "claude"
	}
	cmd := exec.CommandContext(ctx, native, argv...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MITOS_URL="+*base, "MITOS_AGENT_ID="+client.SessionID, "MITOS_AGENT_TOKEN="+client.Token)
	return cmd.Run()
}

type processStream struct {
	io.Reader
	io.Writer
	close func() error
}

func (p *processStream) Close() error { return p.close() }
func claudeHook() error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("hook input exceeds limit")
	}
	client, err := harness.NewClient(os.Getenv("MITOS_URL"), os.Getenv("MITOS_AGENT_ID"), os.Getenv("MITOS_AGENT_TOKEN"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 59*time.Minute)
	defer cancel()
	var event struct {
		Event   string `json:"hook_event_name"`
		Session string `json:"session_id"`
		ToolID  string `json:"tool_use_id"`
		Tools   []struct {
			ID string `json:"tool_use_id"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return errors.New("invalid hook input")
	}
	if event.Event == "SessionEnd" {
		return client.EndThread(ctx, event.Session)
	}
	acknowledge := func(toolID string) error {
		key := harness.SourceKey(event.Session, toolID)
		answer, err := client.Get(ctx, key)
		var status *harness.HTTPError
		if errors.As(err, &status) && (status.Status == 404 || status.Status == 410) {
			return nil
		}
		if err != nil {
			return err
		}
		if answer.State == "pending" {
			return nil
		}
		return client.Deliver(ctx, key, answer.Digest, "applied")
	}
	if event.Event == "PostToolBatch" {
		for _, tool := range event.Tools {
			if err := acknowledge(tool.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if event.Event == "PostToolUse" || event.Event == "PostToolUseFailure" {
		return acknowledge(event.ToolID)
	}
	if _, err = harness.Normalize("claude", raw); err != nil {
		fmt.Println("{}")
		return nil
	}
	submitted, err := client.Submit(ctx, raw)
	if err != nil {
		return err
	}
	answer, err := harness.Wait(ctx, client, submitted.RequestID)
	if err != nil {
		return err
	}
	if _, err = os.Stdout.Write(append(answer.Body, '\n')); err != nil {
		_ = client.Deliver(ctx, submitted.RequestID, answer.Digest, "application_blocked")
		return err
	}
	// Applied is recorded by PostToolUse/Failure or PostToolBatch, after Claude
	// consumes the answer. Batch acknowledgement includes denied calls.
	return nil
}
