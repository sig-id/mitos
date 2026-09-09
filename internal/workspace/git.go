// Package workspace owns bounded workspace copies and Git-backed change records.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Git runs without ambient configuration, hooks, filters, signing, credentials,
// or optional index writes. Callers supply plumbing commands and argv, not shell.
func git(ctx context.Context, dir, gitDir string, input []byte, args ...string) ([]byte, error) {
	prefix := []string{"--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.fsync=all", "-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null", "-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false", "-c", "core.autocrlf=false", "-c", "core.fileMode=true", "-c", "protocol.allow=never", "-c", "safe.directory=" + dir}
	if gitDir != "" {
		prefix = append(prefix, "--git-dir="+gitDir)
	}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_AUTHOR_NAME=Mitos", "GIT_AUTHOR_EMAIL=mitos@localhost", "GIT_COMMITTER_NAME=Mitos", "GIT_COMMITTER_EMAIL=mitos@localhost", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z"}
	cmd.Stdin = bytes.NewReader(input)
	var stdout cappedOutput
	stdout.limit = 256 << 20
	var stderr cappedOutput
	stderr.limit = 64 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("Git output exceeded its limit")
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.buffer.String()))
	}
	return stdout.buffer.Bytes(), nil
}

type cappedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.buffer.Len()
	if n > remaining {
		w.exceeded = true
	}
	_, _ = w.buffer.Write(p[:min(n, remaining)])
	return n, nil
}

func sourcePaths(ctx context.Context, source string) ([]string, error) {
	_, err := git(ctx, source, "", nil, "rev-parse", "--is-inside-work-tree")
	gitDir := ""
	if err != nil {
		// An invalid repository/configuration is a failure, never a fallback that
		// changes which files enter the workspace.
		if !strings.Contains(err.Error(), "not a git repository") {
			return nil, err
		}
		temporary, err := os.MkdirTemp("", "mitos-ignore-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(temporary)
		gitDir = filepath.Join(temporary, "index.git")
		if _, err = git(ctx, source, "", nil, "init", "--bare", "--template=", gitDir); err != nil {
			return nil, err
		}
		// A separate empty index gives plain directories Git's complete nested
		// .gitignore semantics without writing a .git directory into the source.
		b, err := git(ctx, source, gitDir, nil, "--work-tree="+source, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		return splitPaths(b), nil
	}
	b, err := git(ctx, source, "", nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	return splitPaths(b), nil
}
func splitPaths(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func copyBytes(dst io.Writer, data []byte) error {
	_, err := io.Copy(dst, bytes.NewReader(data))
	return err
}
