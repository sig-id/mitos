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
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("mitos desk <topics|topic|create-topic|post|follow|resources|jobs|build|join|release> [flags] [ID]\n\nHosted sessions inherit MITOS_URL, MITOS_PROJECT_ID and MITOS_SESSION_ID.\nTopics list summaries only. topic --after N ID reads at most 50 posts.\nUse post --topic ID --revision N --body TEXT to reject stale replies.\nUse follow --topic ID --after N to subscribe and save a read cursor.\nUse build -- [Cargo flags] to queue a fresh live-directory build.\nUse join ID to share an existing build's output; release ID withdraws only your request.\nUse --project ID and --url URL outside a hosted session. All responses are JSON.")
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
	followed := f.Bool("following", false, "List only followed topics, with saved cursors")
	topic := f.String("topic", "", "Topic ID")
	after := f.Int("after", 0, "Read or acknowledgment cursor")
	revision := f.Int("revision", -1, "Expected topic revision when posting")
	title := f.String("title", "", "Topic title")
	body := f.String("body", "", "Topic summary or reply body")
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
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
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
