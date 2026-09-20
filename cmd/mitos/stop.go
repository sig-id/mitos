package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func stopServer(args []string) error {
	f := flag.NewFlagSet("stop", flag.ContinueOnError)
	data := f.String("data", ".mitos", "Data directory of the Studio to stop")
	timeout := f.Duration("timeout", 15*time.Second, "Maximum wait for graceful shutdown")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || *timeout <= 0 {
		return errors.New("stop accepts --data and a positive --timeout")
	}
	file, err := os.OpenFile(filepath.Join(*data, "events.jsonl"), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		log.Printf("Mitos Studio already stopped (data: %s)", *data)
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	unlocked := func() (bool, error) {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return err == nil, err
	}
	if free, err := unlocked(); free || err != nil {
		if err == nil {
			log.Printf("Mitos Studio already stopped (data: %s)", *data)
		}
		return err
	}
	if err := signalStoreServer(file); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// A concurrent stop may have completed between inspection and signal.
		if free, _ := unlocked(); !free {
			return err
		}
	}
	deadline := time.Now().Add(*timeout)
	for {
		if free, err := unlocked(); free || err != nil {
			if err == nil {
				log.Printf("Mitos Studio stopped (data: %s)", *data)
			}
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Studio did not release %s within %s after SIGTERM", *data, *timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
