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
	"strings"
	"time"

	"mitos/internal/apiversion"
	"mitos/internal/decision"
	"mitos/internal/studio"
)

func principalClient(args []string) error {
	if len(args) == 0 {
		return errors.New("principal requires create, list, rotate or revoke")
	}
	f := flag.NewFlagSet("principal "+args[0], flag.ContinueOnError)
	server := f.String("server", "http://127.0.0.1:4646", "Local operator URL")
	id := f.String("id", "", "Stable service principal ID")
	name := f.String("name", "", "Display name")
	org := f.String("org", "", "Tenant organization")
	project := f.String("project", "", "Tenant project")
	write := f.Bool("write", false, "Permit structured-work admission and cancellation")
	expires := f.Duration("expires", 30*24*time.Hour, "Credential lifetime, at most 366 days")
	var agents []string
	f.Func("agent", "Permitted library agent ID (repeat; default mitos.generalist)", func(value string) error { agents = append(agents, value); return nil })
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected principal arguments")
	}
	path, method := "/api/service-principals", "GET"
	var body any
	switch args[0] {
	case "create":
		method = "POST"
		body = studio.ServicePrincipalInput{ID: *id, Name: *name, Tenant: decision.Tenant{Org: *org, Project: *project}, Read: true, Write: *write, Agents: agents, ExpiresAt: time.Now().Add(*expires)}
	case "list":
	case "rotate":
		if !studio.ValidDeviceID(*id) {
			return errors.New("supply --id")
		}
		method = "POST"
		path += "/" + url.PathEscape(*id) + "/rotate"
		body = map[string]time.Time{"expires_at": time.Now().Add(*expires)}
	case "revoke":
		if !studio.ValidDeviceID(*id) {
			return errors.New("supply --id")
		}
		method = "DELETE"
		path += "/" + url.PathEscape(*id)
	default:
		return errors.New("principal requires create, list, rotate or revoke")
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, strings.TrimRight(*server, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	apiversion.Set(req)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("principal: HTTP %d: %s", resp.StatusCode, result)
	}
	fmt.Print(string(result))
	return nil
}
