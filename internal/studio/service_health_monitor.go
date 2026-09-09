package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mitos/internal/compute"
)

type serviceHealthMonitor struct {
	store    *Store
	runID    string
	scope    *ServiceHealthState
	provider compute.Provider
	launch   compute.LaunchSpec
}

func (m *serviceHealthMonitor) watch(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		state, err := m.store.healthCheckStart(m.runID, m.scope.MonitorID, time.Now().UTC())
		if errors.Is(err, ErrConflict) {
			return nil
		}
		if err != nil {
			return err
		}
		if state == nil {
			continue
		}
		check := *state.Check
		check.Outcome = "healthy"
		switch state.Config.Type {
		case "file":
			root, err := os.OpenRoot(m.launch.Workspace.Directory)
			if err != nil {
				check.Outcome = "unhealthy"
				check.Detail = "Health workspace is unavailable."
			} else {
				info, statErr := root.Stat(state.Config.Path)
				_ = root.Close()
				if statErr != nil || !info.Mode().IsRegular() {
					check.Outcome = "unhealthy"
					check.Detail = "Health file is missing, outside the workspace, or not a regular file."
				} else {
					at := info.ModTime().UTC()
					check.EvidenceAt = &at
					age := time.Since(at)
					if age < 0 || age > time.Duration(state.Config.MaxAge)*time.Second {
						check.Outcome = "unhealthy"
						check.Detail = "Health file timestamp is stale or in the future."
					}
				}
			}
		case "heartbeat":
			evaluateHeartbeat(state, &check, time.Now().UTC())
		case "command":
			duration := min(5*time.Second, time.Duration(state.Config.Interval)*time.Second)
			probeCtx, cancel := context.WithTimeout(ctx, duration)
			launch := m.launch
			launch.Base = filepath.Join(m.store.Dir(), "health", m.runID, state.MonitorID, fmt.Sprint(check.Number))
			launch.Workspace.HistoryDirectory = ""
			launch.Workspace.Source = ""
			launch.Workspace.BaselineCommit = ""
			launch.Workspace.BaselineFingerprint = ""
			launch.Posture.Deadline, _ = probeCtx.Deadline()
			_, output, probeErr := executeProviderCommand(probeCtx, m.provider, launch, []string{"/bin/sh", "-c", state.Config.Command}, launch.Workspace.Directory, nil, nil)
			cancel()
			sum := sha256.Sum256(output)
			check.OutputSHA256 = fmt.Sprintf("%x", sum)
			check.OutputBytes = len(output)
			if probeErr != nil {
				check.Outcome = "unhealthy"
				check.Detail = fmt.Sprintf("Health command failed or exceeded its probe deadline: %.1500s", probeErr.Error())
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		stop, err := m.store.healthCheckFinish(m.runID, state.MonitorID, check)
		if err != nil {
			return err
		}
		if stop {
			if state.Config.OnUnhealthy == "restart" {
				return errHealthRestart
			}
			return errHealthKill
		}
	}
}
