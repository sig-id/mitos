package harness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

var errNativeFrameTooLarge = fmt.Errorf("app-server frame exceeds %d-byte limit", MaxObservationBytes)

// The limit counts JSON bytes, not the optional CRLF line terminator. Scanner
// also needs room to discover EOF after an exactly-at-limit final frame.
func nativeScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), MaxObservationBytes+2)
	s.Split(func(data []byte, eof bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, eof)
		if len(token) > MaxObservationBytes {
			return 0, nil, errNativeFrameTooLarge
		}
		return advance, token, err
	})
	return s
}

func nativeScannerError(s *bufio.Scanner) error {
	if errors.Is(s.Err(), bufio.ErrTooLong) {
		return errNativeFrameTooLarge
	}
	return s.Err()
}

type nativeFrame struct {
	line []byte
	err  error
}

// Deliver the terminal error through the same channel as the frames. A separate
// error channel can race with EOF or overtake queued final-result notifications.
func readNativeFrames(ctx context.Context, r io.Reader, frames chan<- nativeFrame) {
	defer close(frames)
	s := nativeScanner(r)
	for s.Scan() {
		frame := nativeFrame{line: append([]byte(nil), s.Bytes()...)}
		select {
		case frames <- frame:
		case <-ctx.Done():
			return
		}
	}
	err := nativeScannerError(s)
	if err == nil {
		err = io.EOF
	}
	select {
	case frames <- nativeFrame{err: err}:
	case <-ctx.Done():
	}
}
