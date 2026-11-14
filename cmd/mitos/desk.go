package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func deskClient(args []string) error {
	if len(args) > 0 && args[0] == "attention-hook" {
		return attentionHook()
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("mitos desk <work|report-work|profile|coordination|broadcast|inbox|acknowledge|release-notice|agents|inspect-agent|sessions|start-session|send-prompt|stop-session|resume-session|fork-session|handover-session|link-session|topics|topic|create-topic|post|follow|resources|jobs|build|join|release> [flags] [ID]\n\nHosted sessions inherit MITOS_URL, MITOS_PROJECT_ID and MITOS_SESSION_ID.\nTopics list summaries only. topic --after N ID reads at most 50 posts.\nUse post --topic ID --revision N --body TEXT to reject stale replies.\nUse follow --topic ID --after N to subscribe and save a read cursor.\nUse build -- [Cargo flags] to queue a fresh live-directory build.\nUse join ID to share an existing build's output; release ID withdraws only your request.\nUse --project ID and --url URL outside a hosted session. All responses are JSON.")
		return nil
	}
	action := args[0]
	f := flag.NewFlagSet("desk "+action, flag.ContinueOnError)
	base := os.Getenv("MITOS_URL")
	if base == "" {
		base = "http://127.0.0.1:4646"
	}
	endpoint := f.String("url", base, "Studio URL")
	project := f.String("project", os.Getenv("MITOS_PROJECT_ID"), "Project ID")
	author := f.String("session", os.Getenv("MITOS_SESSION_ID"), "Session ID (defaults to local operator outside a hosted session)")
	query := f.String("query", "", "Topic title, summary or path filter")
	agentID := f.String("agent", "", "Discovered agent or application ID")
	profileID := f.String("profile", "", "Named agent environment profile")
	role := f.String("role", "agent", "Agent or orchestrator role")
	prompt := f.String("prompt", "", "Initial or follow-up prompt")
	criteria := f.String("criteria", "", "Completion criteria for newly delegated work")
	expectedRevision := f.Int("expected-revision", 0, "Expected task revision when assigning an executor")
	goalID := f.String("goal", os.Getenv("MITOS_GOAL_ID"), "Fleet goal reference")
	taskID := f.String("task", "", "Fleet task reference")
	handoffTopic := f.String("handoff-topic", "", "Topic containing a handoff prompt")
	mode := f.String("mode", "", "Continuation mode: native or context (default: native when an ID is recorded)")
	nativeID := f.String("native-session", "", "Exact native CLI conversation ID")
	notes := f.String("notes", "", "Handoff notes for context continuation")
	notesFile := f.String("notes-file", "", "Read handoff notes from a UTF-8 file (at most 32 KiB)")
	key := f.String("key", fmt.Sprintf("cli-%d", time.Now().UnixNano()), "Continuation request key; reuse it to retry the same request")
	followed := f.Bool("following", false, "List only followed topics, with saved cursors")
	topic := f.String("topic", "", "Topic ID")
	after := f.Int("after", 0, "Read or acknowledgment cursor")
	revision := f.Int("revision", -1, "Expected topic revision when posting")
	title := f.String("title", "", "Topic title")
	body := f.String("body", "", "Topic summary or reply body")
	waitInbox := f.Bool("wait", false, "Wait up to 20 seconds for inbox attention")
	pause := f.Bool("pause", false, "Request an acknowledged breakpoint until explicitly released")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *author == "" {
		*author = "operator"
	}
	path := ""
	var payload any
	proj := "/api/projects/" + url.PathEscape(*project)
	getID := func() string {
		if *topic != "" {
			return *topic
		}
		if f.NArg() > 0 {
			return f.Arg(0)
		}
		return ""
	}
	switch action {
	case "profile", "coordination":
		path = proj + "/" + action
	case "broadcast":
		path = proj + "/attention"
		payload = map[string]any{"author": *author, "body": *body, "pause": *pause}
	case "inbox":
		if *author == "operator" {
			return errors.New("--session or MITOS_SESSION_ID is required for the agent inbox")
		}
		path = "/api/sessions/" + url.PathEscape(*author) + "/inbox"
		if *waitInbox {
			path += "?wait=true"
		}
	case "acknowledge":
		if f.NArg() != 1 || *author == "operator" {
			return errors.New("provide a notice ID and the acknowledging session")
		}
		path = "/api/sessions/" + url.PathEscape(*author) + "/inbox/" + url.PathEscape(f.Arg(0)) + "/acknowledge"
		payload = map[string]any{}
	case "release-notice":
		if f.NArg() != 1 {
			return errors.New("provide a notice ID")
		}
		path = "/api/attention/" + url.PathEscape(f.Arg(0)) + "/release"
		payload = map[string]string{"author": *author}
	case "agents":
		path = "/api/agents"
		if *project != "" {
			path += "?project=" + url.QueryEscape(*project)
		}
	case "inspect-agent":
		if *agentID == "" {
			return errors.New("--agent required")
		}
		q := url.Values{"profile": {*profileID}, "project": {*project}}
		path = "/api/agents/" + url.PathEscape(*agentID) + "?" + q.Encode()
	case "work":
		path = proj + "/work"
		if *taskID != "" {
			path += "?task=" + url.QueryEscape(*taskID)
		}
	case "report-work":
		if *taskID == "" || *revision < 0 {
			return errors.New("--task and --revision required; read desk work first")
		}
		path = proj + "/work/" + url.PathEscape(*taskID) + "/report"
		payload = map[string]any{"revision": *revision, "session_id": *author, "body": *body}
	case "sessions":
		path = proj + "/sessions"
	case "start-session":
		if *agentID == "" {
			return errors.New("--agent required")
		}
		path = proj + "/sessions"
		payload = map[string]any{"agent_id": *agentID, "profile_id": *profileID, "key": *key, "prompt": *prompt, "role": *role, "criteria": *criteria, "expected_revision": *expectedRevision, "assignment": map[string]string{"goal_id": *goalID, "task_id": *taskID, "requested_by": *author, "handoff_topic_id": *handoffTopic}}
	case "resume-session", "fork-session", "handover-session", "link-session":
		if f.NArg() != 1 {
			return errors.New("provide a source session ID")
		}
		path = "/api/sessions/" + url.PathEscape(f.Arg(0))
		if action == "link-session" {
			path += "/native-session"
			payload = map[string]string{"native_session_id": *nativeID}
		} else {
			if *notesFile != "" {
				if *notes != "" {
					return errors.New("use either --notes or --notes-file")
				}
				file, err := os.Open(*notesFile)
				if err != nil {
					return err
				}
				b, err := io.ReadAll(io.LimitReader(file, (32<<10)+1))
				_ = file.Close()
				if err != nil {
					return err
				}
				if len(b) > 32<<10 {
					return errors.New("handoff notes exceed 32 KiB")
				}
				*notes = string(b)
			}
			path += "/" + strings.TrimSuffix(action, "-session")
			payload = map[string]string{"key": *key, "mode": *mode, "agent_id": *agentID, "profile_id": *profileID, "native_session_id": *nativeID, "prompt": *prompt, "notes": *notes, "handoff_topic_id": *handoffTopic}
		}
	case "send-prompt", "stop-session":
		if f.NArg() != 1 {
			return errors.New("provide a session ID")
		}
		path = "/api/sessions/" + url.PathEscape(f.Arg(0))
		if action == "stop-session" {
			path += "/stop"
			payload = map[string]any{}
		} else {
			path += "/prompt"
			payload = map[string]any{"key": fmt.Sprintf("cli-%d", time.Now().UnixNano()), "text": *prompt}
		}
	case "topics":
		q := url.Values{"q": {*query}}
		if *followed {
			q.Set("follower", *author)
		}
		path = proj + "/topics?" + q.Encode()
	case "topic":
		if getID() == "" {
			return errors.New("topic ID required")
		}
		path = "/api/topics/" + url.PathEscape(getID()) + fmt.Sprintf("?after=%d", *after)
	case "create-topic":
		path = proj + "/topics"
		payload = map[string]any{"title": *title, "summary": *body, "author": *author, "paths": []string{}}
	case "post":
		if *topic == "" || *revision < 0 {
			return errors.New("--topic and --revision are required")
		}
		path = "/api/topics/" + url.PathEscape(*topic) + "/posts"
		payload = map[string]any{"body": *body, "author": *author, "revision": *revision}
	case "follow":
		if *topic == "" {
			return errors.New("--topic is required")
		}
		path = "/api/topics/" + url.PathEscape(*topic) + "/follow"
		payload = map[string]any{"session": *author, "cursor": *after}
	case "resources":
		path = proj + "/resources?details=true"
	case "jobs":
		path = proj + "/jobs"
	case "build":
		path = proj + "/jobs"
		payload = map[string]any{"author": *author, "args": f.Args()}
	case "join", "release":
		if getID() == "" {
			return errors.New("job ID required")
		}
		path = "/api/jobs/" + url.PathEscape(getID()) + "/" + action
		payload = map[string]any{"author": *author}
	default:
		return fmt.Errorf("unknown desk command %q", action)
	}
	if strings.HasPrefix(path, "/api/projects/") && *project == "" {
		return errors.New("set --project or MITOS_PROJECT_ID")
	}
	method := "GET"
	var bodyReader io.Reader
	if payload != nil {
		method = "POST"
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, strings.TrimRight(*endpoint, "/")+path, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	timeout := 15 * time.Second
	if action == "inbox" && *waitInbox {
		timeout = 25 * time.Second
	}
	response, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(b)))
	}
	_, err = os.Stdout.Write(b)
	return err
}
