package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"mitos/internal/messaging"
)

func messageClient(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println("mitos message <status|send|outbox|inbox|ack> [flags]\nUse send --file request.json with a stable id and explicit deadline.\nUse inbox --project ID --mailbox ID [--after SEQUENCE].\nUse outbox --id ID or ack --sender SERVER --id ID --generation N.")
		return nil
	}
	action := args[0]
	f := flag.NewFlagSet("message "+action, flag.ContinueOnError)
	base := os.Getenv("MITOS_URL")
	if base == "" {
		base = "http://127.0.0.1:4646"
	}
	endpoint := f.String("url", base, "Local Mitos HTTP URL")
	file := f.String("file", "", "Message request JSON file (send)")
	id := f.String("id", "", "Stable message ID")
	sender := f.String("sender", "", "Origin server ID (ack)")
	project := f.String("project", "studio", "Project ID")
	mailbox := f.String("mailbox", "operator", "Mailbox ID")
	after := f.Int64("after", 0, "Exclusive journal sequence cursor")
	generation := f.Int("generation", 0, "Current responsibility generation; 0 for operator inbox")
	server := f.String("server", "", "Expected home server identity")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	path := "/api/messaging"
	method := "GET"
	var body []byte
	switch action {
	case "status":
	case "send":
		if *file == "" {
			return errors.New("send requires --file")
		}
		input, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer input.Close()
		body, err = io.ReadAll(io.LimitReader(input, messaging.MaxPacket+1))
		if err != nil {
			return err
		}
		if len(body) > messaging.MaxPacket {
			return errors.New("message request too large")
		}
		var r messaging.Request
		if err = messaging.Decode(body, &r); err != nil {
			return err
		}
		path += "/outbox"
		method = "POST"
	case "outbox":
		if !messaging.ValidID(*id) {
			return errors.New("outbox requires --id")
		}
		path += "/outbox/" + url.PathEscape(*id)
	case "inbox":
		path += "/inbox?" + url.Values{"project": {*project}, "mailbox": {*mailbox}, "after": {strconv.FormatInt(*after, 10)}}.Encode()
	case "ack":
		if !messaging.ValidID(*id) || !messaging.ValidID(*sender) {
			return errors.New("ack requires --id and --sender")
		}
		path += "/inbox/" + url.PathEscape(*sender) + "/" + url.PathEscape(*id) + "/ack"
		method = "POST"
		body = []byte(fmt.Sprintf(`{"generation":%d}`, *generation))
	default:
		return fmt.Errorf("unknown message operation %q", action)
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Host == "" {
		return errors.New("invalid Mitos URL")
	}
	// path contains a query only for inbox; resolve it separately to avoid escaping '?'.
	relative, _ := url.Parse(path)
	u = u.ResolveReference(relative)
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mitos-Client", "cli")
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
