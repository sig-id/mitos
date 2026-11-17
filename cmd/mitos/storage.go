package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mitos/internal/apiversion"
	"mitos/internal/studio"
)

func storageClient(args []string) error {
	if len(args) == 0 {
		return errors.New("storage requires backup, restore or expire-receipts")
	}
	f := flag.NewFlagSet("storage "+args[0], flag.ContinueOnError)
	server := f.String("server", "http://127.0.0.1:4646", "Local operator server URL")
	out := f.String("out", "", "New backup directory on the server")
	from := f.String("from", "", "Backup directory to restore")
	data := f.String("data", "", "New data directory for restore (must not exist)")
	before := f.String("before", "", "Expire terminal request keys last updated before RFC3339 timestamp")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected storage arguments")
	}
	var path string
	var body any
	switch args[0] {
	case "restore":
		if *from == "" || *data == "" {
			return errors.New("restore requires --from and --data")
		}
		return studio.RestoreStore(*from, *data)
	case "backup":
		if *out == "" {
			return errors.New("backup requires --out")
		}
		path = "/api/storage/backup"
		body = map[string]string{"directory": *out}
	case "expire-receipts":
		at, err := time.Parse(time.RFC3339, *before)
		if err != nil {
			return errors.New("expire-receipts requires --before RFC3339")
		}
		path = "/api/storage/expire-receipts"
		body = map[string]time.Time{"before": at}
	default:
		return errors.New("storage requires backup, restore or expire-receipts")
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", strings.TrimRight(*server, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set(apiversion.Header, apiversion.Current)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("storage: HTTP %d: %s", resp.StatusCode, result)
	}
	fmt.Print(string(result))
	return nil
}
