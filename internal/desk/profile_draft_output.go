package desk

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const draftLogLimit = 128 << 10

type ProfileDraftOutput struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// Diagnostics are a rolling tail, independent of the bounded profile response.
// A verbose repository inspection must not cancel an otherwise healthy agent.
type draftLog struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (l *draftLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if len(l.data)+n > draftLogLimit {
		l.truncated = true
		if n >= draftLogLimit {
			l.data = append(l.data[:0], p[n-draftLogLimit:]...)
			return n, nil
		}
		l.data = append(l.data[:0], l.data[len(l.data)+n-draftLogLimit:]...)
	}
	l.data = append(l.data, p...)
	return n, nil
}

func (l *draftLog) snapshot() ProfileDraftOutput {
	l.mu.Lock()
	defer l.mu.Unlock()
	return ProfileDraftOutput{Text: strings.ToValidUTF8(string(l.data), "�"), Truncated: l.truncated}
}

func (m *Manager) ReadProfileDraftOutput(project, id string) (ProfileDraftOutput, error) {
	m.mu.Lock()
	_, err := m.draftLocked(project, id)
	log := m.draftLogs[id]
	m.mu.Unlock()
	if err != nil {
		return ProfileDraftOutput{}, err
	}
	if log != nil {
		return log.snapshot(), nil
	}
	f, err := os.OpenFile(filepath.Join(m.dir, "profile-drafts", id, "output.log"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return ProfileDraftOutput{}, nil
	}
	if err != nil {
		return ProfileDraftOutput{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ProfileDraftOutput{}, err
	}
	if !info.Mode().IsRegular() {
		return ProfileDraftOutput{}, errors.New("analysis output is not a regular file")
	}
	if info.Size() > draftLogLimit {
		if _, err = f.Seek(-draftLogLimit, io.SeekEnd); err != nil {
			return ProfileDraftOutput{}, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, draftLogLimit))
	return ProfileDraftOutput{Text: strings.ToValidUTF8(string(b), "�"), Truncated: info.Size() >= draftLogLimit}, err
}
