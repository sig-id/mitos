package main

import (
	"bytes"
	"context"
	"crypto/tls"
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/device"
	"mitos/internal/fleet"
	"mitos/internal/service"
	"mitos/internal/studio"
	"mitos/internal/web"
	"mitos/internal/workpool"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		// Claude treats exit 1 as a nonblocking hook error. A failed connector
		// must block the waiting tool instead of bypassing canonical approval.
		if len(os.Args) > 2 && os.Args[1] == "agent" && os.Args[2] == "claude-hook" {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "agent":
		return agentClient(args[1:])
	case "device":
		return deviceClient(args[1:])
	case "desk":
		return deskClient(args[1:])
	case "serve":
		return serve(args[1:])
	case "stop":
		return stopServer(args[1:])
	case "run", "status", "log", "interrupts", "checkpoints", "rewind", "decisions", "resolve", "autonomy", "fleet", "evidence", "release-claims", "tasks", "task", "pools", "configure-pool", "services", "services-preview", "services-configure", "service-run", "heartbeat", "service-signal", "emit-event", "service-events", "service-stop", "service-restart", "service-resume", "service-lifecycle":
		return client(args[0], args[1:])
	case "help", "--help", "-h":
		fmt.Println("Mitos Studio\n\n  mitos agent  Connect Codex or Claude waiting requests\n  mitos device  Pair, list, rotate or revoke handheld credentials\n  mitos serve   Restart the local Go + HTMX studio\n  mitos stop    Stop the Studio using a data directory\n  mitos run     Submit a brief to a running studio\n  mitos status  Read a run from a running studio\n  mitos log     Read a run's events\n  mitos interrupts  Report recorded human interruptions\n  mitos checkpoints List a run's saved recovery points\n  mitos autonomy Report autonomy evidence and coverage\n  mitos fleet   List structured fleet status\n  mitos tasks   List durable task state\n  mitos task    Read a task and its attempt lineage\n  mitos pools   List persisted scheduling policies\n  mitos services Read desired service activation and reasons\n  mitos services-preview Validate a service catalog without saving\n  mitos services-configure Save a catalog at an expected revision\n  mitos service-run Trigger a service task with an idempotency key\n  mitos service-signal Deliver a JSON message to an external-signal service\n  mitos service-stop Stop a service or target and its part_of members\n  mitos service-restart Stop a service group and publish fresh Tasks\n  mitos service-resume Release a settled service stop\n  mitos service-lifecycle Read service stop operations\n  mitos emit-event Emit a named JSON event to active subscribers\n  mitos service-events List custom events or read one by sequence\n  mitos heartbeat Record a current run monitor heartbeat\n  mitos configure-pool Save a scheduling policy\n  mitos evidence Read a run’s bounded structured evidence\n  mitos release-claims Release inspected previous execution claims\n  mitos decisions List pending human decisions\n  mitos resolve Record a choice for a decision\n  mitos rewind  Restore a checkpoint of parked work\n\nUse a command with --help for its options.")
		return nil
	default:
		return fmt.Errorf("unknown command %q; use mitos help", args[0])
	}
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := f.String("addr", "127.0.0.1:4646", "Loopback HTTP address")
	deviceAddr := f.String("device-addr", "", "Separate device HTTPS listener (disabled by default)")
	deviceCert := f.String("device-cert", "", "Device TLS certificate PEM with its chain")
	deviceKey := f.String("device-key", "", "Device TLS private key PEM")
	sttURL := f.String("device-stt-url", "", "Audio transcription endpoint (HTTPS or local HTTP)")
	sttModel := f.String("device-stt-model", "whisper-1", "Audio transcription model")
	sttCommand := f.String("device-stt-command", "", "STT executable: PCM16 stdin, transcript stdout")
	data := f.String("data", ".mitos", "Durable data directory")
	root := f.String("workspace-root", ".", "Default workspace and base for relative project paths")
	appsDir := f.String("apps", "", "Directory containing local application folders")
	demo := f.Bool("demo", false, "Seed labelled example history in an empty data directory")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *deviceAddr == "" && (*deviceCert != "" || *deviceKey != "" || *sttCommand != "" || *sttURL != "") {
		return errors.New("device TLS/STT flags require --device-addr")
	}
	var speech device.Transcriber
	if *sttURL != "" && *sttCommand != "" {
		return errors.New("choose either --device-stt-url or --device-stt-command")
	}
	if *sttURL != "" {
		provider, err := device.NewHTTPTranscriber(*sttURL, *sttModel, os.Getenv("MITOS_STT_API_KEY"))
		if err != nil {
			return err
		}
		speech = provider
	}
	var deviceTLS *tls.Config
	if *deviceAddr != "" {
		if *deviceCert == "" || *deviceKey == "" {
			return errors.New("--device-addr requires --device-cert and --device-key")
		}
		cert, err := tls.LoadX509KeyPair(*deviceCert, *deviceKey)
		if err != nil {
			return err
		}
		deviceTLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
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
	if err := stopServer([]string{"--data", *data}); err != nil {
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
	discovery, discoveryCancel := context.WithTimeout(context.Background(), 4*time.Second)
	engine.Desk.Agents.Refresh(discovery)
	discoveryCancel()
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
	// WebSockets are hijacked connections, so HTTP shutdown does not own them.
	server.RegisterOnShutdown(handler.CloseStreams)
	defer handler.CloseStreams()
	serverErr := make(chan error, 2)
	var deviceServer *http.Server
	if deviceTLS != nil {
		stt := speech
		if *sttCommand != "" {
			stt = device.CommandTranscriber{Path: *sttCommand}
		}
		deviceServer = &http.Server{Addr: *deviceAddr, Handler: device.New(engine, stt), TLSConfig: deviceTLS, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
		remote, err := net.Listen("tcp", *deviceAddr)
		if err != nil {
			listener.Close()
			return err
		}
		defer deviceServer.Close()
		go func() { serverErr <- deviceServer.Serve(tls.NewListener(remote, deviceTLS)) }()
		log.Printf("Mitos device API → https://%s", remote.Addr())
	}
	defer server.Close()
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
	if deviceServer != nil {
		if err := deviceServer.Shutdown(shutdown); err != nil {
			return err
		}
	}
	return server.Shutdown(shutdown)
}
func client(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	catalogFile := f.String("file", "", "Service catalog, signal, or event payload JSON file")
	signalSource := f.String("source", "", "Configured external signal source")
	revision := f.Int64("expected-revision", -1, "Required current service catalog revision when saving")
	healthMonitor := f.String("monitor", "", "Current service health monitor ID")
	heartbeatSequence := f.Uint64("sequence", 0, "Increasing heartbeat sequence; duplicates do not refresh the clock")
	requestKey := f.String("key", "", "Idempotency key for a service trigger, signal, or event")
	serviceCycle := f.String("cycle", "", "Shared service cycle key for completion dependencies")
	project := f.String("project", "", "Project service view; this local Studio supports local/studio")
	pool := f.String("pool", "interactive", "Work pool")
	priority := f.Int("priority", 0, "Task priority from -1000 to 1000")
	join := f.String("join", "all", "Dependency join: all, any, or k_of_n")
	quorum := f.Int("quorum", 0, "Required count for k_of_n")
	deadline := f.String("deadline", "", "Absolute task deadline in RFC3339 format, including timezone")
	var dependencies []workpool.Dependency
	f.Func("input", "Consume TASK_ID:ARTIFACT_NAME from a successful task; repeat for multiple artifacts", func(value string) error { return workpool.AddInput(&dependencies, value) })
	f.Func("after", "Wait for successful completion of this task ID; repeat for multiple prerequisites", func(value string) error {
		dependencies = append(dependencies, workpool.Dependency{From: value, Guard: "done"})
		return nil
	})
	f.Func("after-failure", "Wait for failure of this task ID", func(value string) error {
		dependencies = append(dependencies, workpool.Dependency{From: value, Guard: "failed"})
		return nil
	})
	discipline := f.String("discipline", "fifo", "Pool policy: fifo, lifo, priority, deadline_edf")
	concurrency := f.Int("concurrency", 2, "Maximum managed executions in this pool")
	reserved := f.Int("reserved", 0, "Provider slots reserved for this pool")
	var ownedPaths, leases []string
	f.Func("own-path", "Owned relative path prefix; repeat for multiple paths (default: whole workspace for command applications)", func(value string) error { ownedPaths = append(ownedPaths, value); return nil })
	f.Func("lease", "Exclusive resource name; repeat for multiple resources", func(value string) error { leases = append(leases, value); return nil })
	confirmedStopped := f.Bool("confirmed-stopped", false, "Confirm that you inspected and stopped the previous execution before releasing its claims")
	answerValue := f.String("value", "", "String answer for a value decision")
	choice := f.String("choice", "", "Option ID from the saved decision card")
	note := f.String("note", "", "Optional resolution note")
	checkpoint := f.String("checkpoint", "", "Checkpoint ID; defaults to the last passing checkpoint")
	scope := f.String("scope", "step", "Recovery scope: step or tail")
	memo := f.String("memo", "", "Required failure memo carried to the next segment")
	signal := f.String("reason", "operator_recovery", "Recorded recovery reason")
	fromSegment := f.Int("from-segment", -1, "Current segment number, required to reject stale requests")
	allowNonGreen := f.Bool("allow-non-green", false, "Explicitly permit a selected checkpoint without a passing predicate")
	logs := f.String("logs", "", "Read interruption evidence from a log file or directory without modifying it")
	url := f.String("url", "http://127.0.0.1:4646", "Studio URL")
	title := f.String("title", "", "Work title")
	intent := f.String("brief", "", "Desired outcome and constraints")
	workspace := f.String("workspace", ".", "Workspace directory (default workspace or an explicitly opened project)")
	app := f.String("app", "workspace-inventory", "Installed application ID")
	minutes := f.Int("minutes", 10, "Execution time limit")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *logs != "" {
		var report any
		var err error
		switch command {
		case "interrupts":
			report, err = studio.AuditInterruptLogs(*logs)
		case "autonomy":
			report, err = studio.AuditAutonomyLogs(*logs)
		default:
			return errors.New("--logs is only supported by interrupts and autonomy")
		}
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	base := strings.TrimRight(*url, "/")
	var req *http.Request
	var err error
	if command == "run" {
		b := studio.Brief{Title: *title, Intent: *intent, Workspace: *workspace, AppID: *app, TimeoutMinutes: *minutes}
		b.Ownership = fleet.Ownership{Paths: ownedPaths, Leases: leases}
		b.Scheduling = workpool.Request{Pool: *pool, Priority: *priority, Deps: dependencies, Join: workpool.Join{Rule: *join, K: *quorum}}
		if *deadline != "" {
			at, parseErr := time.Parse(time.RFC3339, *deadline)
			if parseErr != nil {
				return parseErr
			}
			b.Scheduling.Deadline = &at
		}
		if err = b.Validate(); err != nil {
			return err
		}
		body, _ := json.Marshal(b)
		req, err = http.NewRequest("POST", base+"/api/runs", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "services" || (command == "status" && *project != "") {
		if *project != "" && *project != "local/studio" {
			return errors.New("this Studio exposes only the local/studio project")
		}
		req, err = http.NewRequest("GET", base+"/api/services", nil)
	} else if command == "services-preview" || command == "services-configure" {
		if *catalogFile == "" {
			return errors.New("--file is required")
		}
		file, openErr := os.Open(*catalogFile)
		if openErr != nil {
			return openErr
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (32<<10)+1))
		file.Close()
		if readErr != nil {
			return readErr
		}
		if len(data) > 32<<10 {
			return errors.New("service catalog exceeds the local HTTP input limit")
		}
		var catalog studio.ServiceCatalog
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&catalog); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("supply one catalog JSON object")
		}
		method, path := "POST", "/api/services/preview"
		if command == "services-configure" {
			if *revision < 0 {
				return errors.New("--expected-revision is required")
			}
			method, path = "PUT", "/api/services"
			data, err = json.Marshal(map[string]any{"expected_revision": *revision, "catalog": catalog})
			if err != nil {
				return err
			}
		}
		req, err = http.NewRequest(method, base+path, bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "service-stop" || command == "service-restart" || command == "service-resume" {
		if f.NArg() != 1 || !service.ValidID(f.Arg(0)) || *requestKey == "" {
			return errors.New("supply --key and one service or target ID")
		}
		data, _ := json.Marshal(map[string]string{"action": strings.TrimPrefix(command, "service-"), "idempotency_key": *requestKey})
		req, err = http.NewRequest("POST", base+"/api/services/"+f.Arg(0)+"/lifecycle", bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "service-events" || command == "service-lifecycle" {
		if f.NArg() > 1 {
			return errors.New("supply at most one receipt sequence")
		}
		path := "/api/" + command
		if f.NArg() == 1 {
			seq, parseErr := strconv.ParseInt(f.Arg(0), 10, 64)
			if parseErr != nil || seq < 1 {
				return errors.New("invalid receipt sequence")
			}
			path += "/" + strconv.FormatInt(seq, 10)
		}
		req, err = http.NewRequest("GET", base+path, nil)
	} else if command == "service-signal" || command == "emit-event" {
		if f.NArg() != 1 || !service.ValidID(f.Arg(0)) || (command == "service-signal" && !service.ValidID(*signalSource)) || *requestKey == "" || *catalogFile == "" {
			if command == "emit-event" {
				return errors.New("supply --key, --file, and one custom event name")
			}
			return errors.New("supply --source, --key, --file, optional --cycle, and one service ID")
		}
		if command == "emit-event" && (!service.ValidCustomEventName(f.Arg(0)) || *serviceCycle != "" || *signalSource != "") {
			return errors.New("use a nonreserved event name; event emission does not take --cycle or --source")
		}
		file, openErr := os.Open(*catalogFile)
		if openErr != nil {
			return openErr
		}
		payload, readErr := io.ReadAll(io.LimitReader(file, studio.MaxIngressBytes+1))
		_ = file.Close()
		if readErr != nil {
			return readErr
		}
		payload, readErr = studio.NormalizeIngressPayload(payload)
		if readErr != nil {
			return readErr
		}
		envelope := map[string]any{"source": *signalSource, "delivery_id": *requestKey, "cycle": *serviceCycle, "payload": json.RawMessage(payload)}
		path := "/api/services/" + f.Arg(0) + "/signals"
		if command == "emit-event" {
			envelope = map[string]any{"name": f.Arg(0), "idempotency_key": *requestKey, "payload": json.RawMessage(payload)}
			path = "/api/service-events"
		}
		data, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			return marshalErr
		}
		if len(data) > studio.MaxIngressBytes {
			return errors.New("event or signal envelope exceeds 64 KiB")
		}
		req, err = http.NewRequest("POST", base+path, bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "heartbeat" {
		if f.NArg() != 1 || strings.ContainsAny(f.Arg(0), "/?.") || *healthMonitor == "" || *heartbeatSequence == 0 {
			return errors.New("supply --monitor, --sequence, and one run ID")
		}
		data, _ := json.Marshal(map[string]any{"monitor_id": *healthMonitor, "sequence": *heartbeatSequence})
		req, err = http.NewRequest("POST", base+"/api/runs/"+f.Arg(0)+"/heartbeat", bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "service-run" {
		if f.NArg() != 1 || !service.ValidID(f.Arg(0)) || *requestKey == "" {
			return errors.New("supply --key and one service ID")
		}
		data, _ := json.Marshal(map[string]string{"idempotency_key": *requestKey, "cycle": *serviceCycle})
		req, err = http.NewRequest("POST", base+"/api/services/"+f.Arg(0)+"/trigger", bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "tasks" || command == "pools" {
		req, err = http.NewRequest("GET", base+"/api/"+command, nil)
	} else if command == "task" {
		if f.NArg() != 1 || strings.ContainsAny(f.Arg(0), "/?.") {
			return errors.New("supply one task ID")
		}
		req, err = http.NewRequest("GET", base+"/api/tasks/"+f.Arg(0), nil)
	} else if command == "configure-pool" {
		if f.NArg() != 1 {
			return errors.New("supply one pool ID")
		}
		p := workpool.Pool{ID: f.Arg(0), Discipline: *discipline, Concurrency: *concurrency, Reserved: *reserved}
		if err = p.Validate(); err != nil {
			return err
		}
		body, _ := json.Marshal(p)
		req, err = http.NewRequest("PUT", base+"/api/pools/"+p.ID, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else if command == "fleet" {
		req, err = http.NewRequest("GET", base+"/api/fleet", nil)
	} else if command == "autonomy" {
		req, err = http.NewRequest("GET", base+"/api/autonomy", nil)
	} else if command == "decisions" {
		req, err = http.NewRequest("GET", base+"/api/decisions", nil)
	} else if command == "resolve" {
		if f.NArg() != 2 || *choice == "" {
			return errors.New("supply --choice OPTION followed by the run ID and decision ID")
		}
		for _, id := range f.Args() {
			if strings.ContainsAny(id, "/?.") {
				return errors.New("invalid run or decision ID")
			}
		}
		answer := map[string]any{"choice": *choice, "note": *note}
		f.Visit(func(option *flag.Flag) {
			if option.Name == "value" {
				answer["value"] = *answerValue
			}
		})
		body, _ := json.Marshal(answer)
		req, err = http.NewRequest("POST", base+"/api/runs/"+f.Arg(0)+"/decisions/"+f.Arg(1), bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Mitos-Client", "cli")
		}
	} else if command == "interrupts" {
		req, err = http.NewRequest("GET", base+"/api/interrupts", nil)
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
		if command == "checkpoints" {
			path += "/checkpoints"
		}
		if command == "evidence" {
			path += "/evidence"
		}
		if command == "release-claims" {
			if !*confirmedStopped {
				return errors.New("--confirmed-stopped is required after inspecting and stopping the previous execution")
			}
			req, err = http.NewRequest("POST", base+path+"/claims/release", strings.NewReader(`{"confirmed_stopped":true}`))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
		} else if command == "rewind" {
			if *fromSegment < 0 {
				return errors.New("--from-segment is required; read the current run first")
			}
			request := autonomy.Rewind{CheckpointID: *checkpoint, Scope: *scope, Memo: *memo, ReasonSignal: *signal, FromSegment: *fromSegment, AllowNonGreen: *allowNonGreen}
			if err = request.Validate(); err != nil {
				return err
			}
			body, _ := json.Marshal(map[string]any{
				"checkpoint_id": request.CheckpointID, "scope": request.Scope,
				"memo": request.Memo, "reason_signal": request.ReasonSignal,
				"from_segment": request.FromSegment, "allow_non_green": request.AllowNonGreen,
			})
			req, err = http.NewRequest("POST", base+path+"/rewind", bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
		} else {
			req, err = http.NewRequest("GET", base+path, nil)
		}
	}
	if err != nil {
		return err
	}
	req.Header.Set("X-Mitos-Client", "cli")
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
