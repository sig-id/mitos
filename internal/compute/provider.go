package compute

import (
	"context"
	"errors"
	"io"
	"time"

	workspacefs "mitos/internal/workspace"
)

var ErrUnsupported = errors.New("provider operation is unsupported")
var ErrAlreadyStarted = errors.New("prepared execution has already started")

type Handle interface {
	ID() string
	Workspace() string
}
type Session interface {
	ID() string
	PID() int // zero when process observation is not available locally
	Done() <-chan struct{}
	Result() ([]byte, error) // after Done; output bytes never imply a verifier verdict
	OutputOffset() uint64
}
type Command struct {
	// Interact drives a structured stdio protocol under provider process ownership.
	Interact func(context.Context, io.ReadWriteCloser, io.Writer) error `json:"-"`
	// NativeEnv is resolved at launch, never serialized or persisted.
	NativeEnv []string `json:"-"`
	Directory string
	Argv      []string
	Input     []byte
}
type Event struct {
	Offset   uint64    `json:"offset"`
	Type     string    `json:"type"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
	ExitCode *int      `json:"exit_code,omitempty"`
}
type Harvest struct {
	Change *workspacefs.Harvest
	Output []byte
}
type Provider interface {
	Capabilities() Capabilities
	Prepare(context.Context, LaunchSpec) (Handle, error)
	Start(context.Context, Handle, Command) (Session, error)
	Stream(context.Context, Session, uint64) (<-chan Event, error)
	Control(context.Context, Session, string) error
	Snapshot(context.Context, Session) (string, error)
	Harvest(context.Context, Session) (Harvest, error)
	Stop(context.Context, Session) error
}
