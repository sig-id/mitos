package agentcli

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

var nativeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var openCodeID = regexp.MustCompile(`^ses_[a-zA-Z0-9]{8,128}$`)

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Exact IDs avoid silently continuing another worker's most recent conversation.
func (r *Registry) ValidateSessionID(agent, reference string) error {
	a, err := r.adapter(agent)
	if err != nil {
		return err
	}
	return validateSessionID(a.Definition().SessionProtocol, reference)
}

func validateSessionID(protocol, reference string) error {
	if protocol == "" {
		return errors.New("this adapter has no native session support; use context handover")
	}
	if protocol == "opencode" && openCodeID.MatchString(reference) {
		return nil
	}
	if protocol != "opencode" && nativeUUID.MatchString(reference) {
		return nil
	}
	return errors.New("provide the exact native conversation ID (UUID, or ses_… for OpenCode)")
}

func (c CLI) SessionArguments(mode, reference, prompt string) ([]string, error) {
	if mode == "new" {
		if reference == "" {
			return c.Arguments(prompt)
		}
		if err := validateSessionID(c.Spec.SessionProtocol, reference); err != nil {
			return nil, err
		}
		if c.Spec.SessionProtocol != "claude" && c.Spec.SessionProtocol != "pi" {
			return nil, errors.New("adapter cannot assign a new conversation ID")
		}
		c.Spec.Args = append(append([]string{}, c.Spec.Args...), "--session-id", reference)
		return c.Arguments(prompt)
	}
	if mode != "resume" && mode != "fork" {
		return nil, errors.New("unknown native session operation")
	}
	if err := validateSessionID(c.Spec.SessionProtocol, reference); err != nil {
		return nil, err
	}
	args := append([]string{}, c.Spec.Args...)
	switch c.Spec.SessionProtocol {
	case "codex":
		args = append(args, mode, reference)
	case "claude":
		args = append(args, "--resume", reference)
		if mode == "fork" {
			args = append(args, "--fork-session")
		}
	case "opencode":
		args = append(args, "--session", reference)
		if mode == "fork" {
			args = append(args, "--fork")
		}
	case "pi":
		flag := "--session"
		if mode == "fork" {
			flag = "--fork"
		}
		args = append(args, flag, reference)
	}
	c.Spec.Args = args
	return c.Arguments(prompt)
}
