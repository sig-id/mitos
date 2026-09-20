package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mitos/internal/studio"
)

func deviceClient(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println("mitos device <pair|list|rotate|revoke> [flags] [DEVICE_ID]\n\nPairing and rotation print the token once. Provision it over USB.\nUse --help after a subcommand for its options.")
		return nil
	}
	action := args[0]
	f := flag.NewFlagSet("device "+action, flag.ContinueOnError)
	endpoint := f.String("url", "http://127.0.0.1:4646", "Loopback Studio URL")
	name := f.String("name", "Handheld", "Device name")
	ttl := f.Duration("ttl", 30*24*time.Hour, "Credential lifetime (at most 366 days)")
	resolve := f.Bool("resolve", true, "Permit confirming offered decisions")
	voice := f.Bool("voice", false, "Permit voice drafts")
	disclosure := f.String("stt-disclosure", "", "Required for voice: explain provider routing and retention")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*endpoint)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return errors.New("device administration requires the loopback Studio URL")
	}
	method, path := "GET", "/api/devices"
	var body any
	switch action {
	case "list":
		if f.NArg() != 0 {
			return errors.New("list takes no device ID")
		}
	case "pair":
		if f.NArg() != 0 {
			return errors.New("pair takes no device ID")
		}
		method = "POST"
		body = studio.DevicePairInput{Name: *name, Permissions: studio.DevicePermissions{Read: true, Resolve: *resolve, Voice: *voice}, ExpiresAt: time.Now().UTC().Add(*ttl), STTDisclosure: *disclosure}
	case "rotate", "revoke":
		if f.NArg() != 1 || !studio.ValidDeviceID(f.Arg(0)) {
			return errors.New("one valid device ID is required")
		}
		method = "POST"
		path += "/" + f.Arg(0) + "/" + action
		if action == "rotate" {
			body = map[string]any{"expires_at": time.Now().UTC().Add(*ttl)}
		}
	default:
		return fmt.Errorf("unknown device action %q", action)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, strings.TrimRight(*endpoint, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Studio returned %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	fmt.Println(string(data))
	return nil
}
