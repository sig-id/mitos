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

	"mitos/internal/apiversion"
)

func watchClient(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("mitos watch <create|list|show|renew|cancel|deliveries|items|triage|resolve|inbox|outcome|report> [flags]\nUse create --file watch.json (mitos.watch/v1; operator, responsibility or attempt owner).\nUse show, renew --expires RFC3339, cancel, deliveries, items, triage or report with --id WATCH_ID.\nUse resolve --item ITEM_ID to close an open Notifications item.\nUse inbox --owner responsibility:GRAPH_ID or attempt:RUN_ID to read an owner's pending deliveries.\nUse outcome --delivery DELIVERY_ID --outcome acted|dismissed|deferred, or --mark should_have_woken|needless_wake.")
		return nil
	}
	action := args[0]
	f := flag.NewFlagSet("watch "+action, flag.ContinueOnError)
	base := os.Getenv("MITOS_URL")
	if base == "" {
		base = "http://127.0.0.1:4646"
	}
	endpoint := f.String("url", base, "Local Mitos HTTP URL")
	file := f.String("file", "", "Watch specification JSON file (create)")
	id := f.String("id", "", "Watch ID")
	item := f.String("item", "", "Notifications item ID (resolve)")
	owner := f.String("owner", "", "Owner as responsibility:GRAPH_ID or attempt:RUN_ID (inbox)")
	delivery := f.String("delivery", "", "Delivery ID (outcome)")
	outcome := f.String("outcome", "", "Owner outcome: acted, dismissed or deferred (outcome)")
	mark := f.String("mark", "", "Operator mark: should_have_woken or needless_wake (outcome)")
	expires := f.String("expires", "", "New lease expiry, RFC3339, at most 24 hours away (renew)")
	limit := f.Int("limit", 50, "Maximum deliveries to list (1–500)")
	server := f.String("server", "", "Expected home server identity")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	needID := func() error {
		if *id == "" {
			return fmt.Errorf("%s requires --id", action)
		}
		return nil
	}
	method, path := "GET", "/api/watches"
	var body []byte
	switch action {
	case "list":
	case "create":
		if *file == "" {
			return errors.New("create requires --file")
		}
		input, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer input.Close()
		body, err = io.ReadAll(io.LimitReader(input, (64<<10)+1))
		if err != nil {
			return err
		}
		if len(body) > 64<<10 {
			return errors.New("watch specification too large")
		}
		method = "POST"
	case "show":
		if err := needID(); err != nil {
			return err
		}
		path += "/" + url.PathEscape(*id)
	case "renew":
		if err := needID(); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339, *expires)
		if err != nil {
			return errors.New("renew requires --expires as RFC3339")
		}
		body, _ = json.Marshal(map[string]time.Time{"expires_at": at})
		method, path = "POST", path+"/"+url.PathEscape(*id)+"/renew"
	case "cancel":
		if err := needID(); err != nil {
			return err
		}
		method, path = "POST", path+"/"+url.PathEscape(*id)+"/cancel"
	case "deliveries":
		if err := needID(); err != nil {
			return err
		}
		path += fmt.Sprintf("/%s/deliveries?limit=%d", url.PathEscape(*id), *limit)
	case "items":
		if err := needID(); err != nil {
			return err
		}
		path += "/" + url.PathEscape(*id) + "/items"
	case "triage":
		if err := needID(); err != nil {
			return err
		}
		path += "/" + url.PathEscape(*id) + "/triage"
	case "resolve":
		if *item == "" {
			return errors.New("resolve requires --item")
		}
		method, path = "POST", "/api/watch-items/"+url.PathEscape(*item)+"/resolve"
	case "inbox":
		kind, ownerID, ok := strings.Cut(*owner, ":")
		if !ok || ownerID == "" {
			return errors.New("inbox requires --owner responsibility:GRAPH_ID or attempt:RUN_ID")
		}
		path = "/api/watch-inboxes/" + url.PathEscape(kind) + "/" + url.PathEscape(ownerID)
	case "outcome":
		if *delivery == "" || (*outcome == "") == (*mark == "") {
			return errors.New("outcome requires --delivery and exactly one of --outcome or --mark")
		}
		if *outcome != "" {
			body, _ = json.Marshal(map[string]string{"outcome": *outcome})
		} else {
			body, _ = json.Marshal(map[string]string{"mark": *mark})
		}
		method, path = "POST", "/api/deliveries/"+url.PathEscape(*delivery)+"/outcome"
	case "report":
		if err := needID(); err != nil {
			return err
		}
		path += "/" + url.PathEscape(*id) + "/report"
	default:
		return fmt.Errorf("unknown watch operation %q", action)
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Host == "" {
		return errors.New("invalid Mitos URL")
	}
	relative, _ := url.Parse(path)
	u = u.ResolveReference(relative)
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mitos-Client", "cli")
	apiversion.Set(req)
	if *server != "" {
		req.Header.Set("X-Mitos-Server", *server)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Mitos returned %d: %s", resp.StatusCode, data)
	}
	_, err = os.Stdout.Write(data)
	return err
}
