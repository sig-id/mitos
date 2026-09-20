package desk

import "io"

// OutputChanges is a coalescing wakeup, not a byte queue. Subscribe before
// ReadOutput so a write between reading and waiting cannot be missed. Each
// consumer keeps its own file offset; slow viewers cannot hold up the PTY.
func (m *Manager) OutputChanges() <-chan struct{} {
	m.outputMu.Lock()
	defer m.outputMu.Unlock()
	return m.outputChanged
}

func (m *Manager) notifyOutput() {
	m.outputMu.Lock()
	defer m.outputMu.Unlock()
	close(m.outputChanged)
	m.outputChanged = make(chan struct{})
}

type outputWriter struct {
	io.Writer
	notify func()
}

func (w outputWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.notify()
	}
	return n, err
}
