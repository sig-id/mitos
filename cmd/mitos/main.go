package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mitos/internal/studio"
	"mitos/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "run", "status", "log":
		return client(args[0], args[1:])
	case "help", "--help", "-h":
		fmt.Println("Mitos Studio\n\n  mitos serve   Start the local Go + HTMX studio\n  mitos run     Submit a brief to a running studio\n  mitos status  Read a run from a running studio\n  mitos log     Read a run's events\n\nUse a command with --help for its options.")
		return nil
	default:
		return fmt.Errorf("unknown command %q; use mitos help", args[0])
	}
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := f.String("addr", "127.0.0.1:4646", "Loopback HTTP address")
	data := f.String("data", ".mitos", "Durable data directory")
	root := f.String("workspace-root", ".", "Allowed workspace parent directory")
	appsDir := f.String("apps", "", "Directory containing local application folders")
	demo := f.Bool("demo", false, "Seed labelled example history in an empty data directory")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("Studio currently supports loopback addresses only; use 127.0.0.1:4646")
	}
	apps, err := studio.LoadApps(*appsDir)
	if err != nil {
		return err
	}
	store, err := studio.OpenStore(*data)
	if err != nil {
		return err
	}
	defer store.Close()
	if *demo {
		if err = studio.SeedDemo(store); err != nil {
			return err
		}
	}
	engine, err := studio.NewEngine(store, apps, *root)
	if err != nil {
		return err
	}
	defer engine.Close()
	handler, err := web.New(engine, *demo)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	log.Printf("Mitos Studio → http://%s  (data: %s)", listener.Addr(), store.Dir())
	select {
	case err = <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
func client(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	url := f.String("url", "http://127.0.0.1:4646", "Studio URL")
	title := f.String("title", "", "Work title")
	intent := f.String("brief", "", "Desired outcome and constraints")
	workspace := f.String("workspace", ".", "Workspace under the server's workspace root")
	app := f.String("app", "workspace-inventory", "Installed application ID")
	minutes := f.Int("minutes", 10, "Execution time limit")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	base := strings.TrimRight(*url, "/")
	var req *http.Request
	var err error
	if command == "run" {
		b := studio.Brief{Title: *title, Intent: *intent, Workspace: *workspace, AppID: *app, TimeoutMinutes: *minutes}
		if err = b.Validate(); err != nil {
			return err
		}
		body, _ := json.Marshal(b)
		req, err = http.NewRequest("POST", base+"/api/runs", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		if f.NArg() != 1 {
			return errors.New("supply one run ID after the flags")
		}
		id := f.Arg(0)
		if strings.ContainsAny(id, "/?.") {
			return errors.New("invalid run ID")
		}
		path := "/api/runs/" + id
		if command == "log" {
			path += "/events"
		}
		req, err = http.NewRequest("GET", base+path, nil)
	}
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 30 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach Studio; start mitos serve first: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("Studio: %s", strings.TrimSpace(string(b)))
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, b, "", "  ") == nil {
		fmt.Println(pretty.String())
	} else {
		fmt.Println(string(b))
	}
	return nil
}
