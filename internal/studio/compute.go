package studio

import (
	"context"
	"errors"
	"time"

	"mitos/internal/compute"
	"mitos/internal/resourcepolicy"
)

func compileApplication(app Application) (compute.LaunchSpec, error) {
	local := &compute.Local{}
	request := compute.Request{}
	development := app.Compute == nil
	if app.Compute != nil {
		request = *app.Compute
	}
	return compute.Compile(request, map[string]compute.Capabilities{"local": local.Capabilities()}, development)
}

// Automatic workflows use native local agents, while preserving their frozen
// per-agent resource request through the same provider admission contract.
func compileWorkflowLaunch(resources resourcepolicy.Agent) (compute.LaunchSpec, error) {
	local := &compute.Local{}
	return compute.Compile(compute.Request{Resources: resources}, map[string]compute.Capabilities{"local": local.Capabilities()}, true)
}

func executeProviderCommand(ctx context.Context, provider compute.Provider, launch compute.LaunchSpec, argv []string, dir string, input []byte, monitor *commandObservation, health ...*serviceHealthMonitor) (compute.Session, []byte, error) {
	var hm *serviceHealthMonitor
	if len(health) > 0 {
		hm = health[0]
	}
	if hm != nil {
		defer func() { _ = hm.store.closeHealth(hm.runID, hm.scope.MonitorID, "The command monitor ended.") }()
	}
	if provider == nil {
		return nil, nil, errors.New("execution provider is not configured")
	}
	handle, err := provider.Prepare(ctx, launch)
	if err != nil {
		return nil, nil, err
	}
	if monitor != nil {
		if err = monitor.prepare(launch.Base); err != nil {
			return nil, nil, monitor.fail(err)
		}
		defer monitor.close()
	}
	session, err := provider.Start(ctx, handle, compute.Command{Argv: argv, Directory: dir, Input: input})
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = provider.Stop(stopCtx, session)
	}()
	var healthResults <-chan error
	healthPending := false
	var resumeHealth func()
	if hm != nil {
		healthCtx, cancelHealth := context.WithCancel(ctx)
		result := make(chan error, 1)
		resumeHealth = func() {
			healthResults = result
			healthPending = true
			go func() { result <- hm.watch(healthCtx) }()
		}
		resumeHealth()
		defer func() {
			cancelHealth()
			if healthPending {
				<-result
			}
		}()
	}
	finish := func() (compute.Session, []byte, error) {
		output, err := session.Result()
		if monitor != nil && ctx.Err() == nil {
			if observed := monitor.finish(); observed != nil {
				err = observed
			}
		}
		return session, output, err
	}
	if monitor == nil && hm == nil {
		<-session.Done()
		return finish()
	}
	if monitor != nil && session.PID() == 0 {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = provider.Stop(stopCtx, session)
		return session, nil, monitor.fail(errors.New("provider has no admitted local observation transport"))
	}
	if monitor != nil {
		monitor.activity.SetPID(session.PID())
	}
	period := 100 * time.Millisecond
	if monitor != nil {
		period = min(period, monitor.thresholds.Snap/2)
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-session.Done():
			return finish()
		case healthErr := <-healthResults:
			healthPending = false
			healthResults = nil
			if healthErr == nil {
				if closeErr := hm.store.closeHealth(hm.runID, hm.scope.MonitorID, "The health policy was withdrawn or the monitor stopped."); closeErr != nil {
					healthErr = closeErr
				} else {
					continue
				}
			}
			select {
			case <-session.Done():
				return finish()
			default:
			}
			if errors.Is(healthErr, errHealthRestart) || errors.Is(healthErr, errHealthKill) {
				action, resume, actionErr := hm.store.healthAction(hm.runID, hm.scope.MonitorID)
				if actionErr != nil {
					healthErr = actionErr
				} else if resume {
					resumeHealth()
					continue
				} else if action == "" {
					if closeErr := hm.store.closeHealth(hm.runID, hm.scope.MonitorID, "The execution or policy changed before the health action."); closeErr != nil {
						healthErr = closeErr
					} else {
						continue
					}
				}
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
			stopErr := provider.Stop(stopCtx, session)
			cancel()
			if stopErr != nil {
				return session, nil, errors.Join(healthErr, stopErr)
			}
			output, _ := session.Result()
			return session, output, healthErr
		case <-ticker.C:
			if monitor == nil {
				continue
			}
			select {
			case <-session.Done():
				return finish()
			default:
			}
			if ctx.Err() != nil {
				continue
			}
			if err = monitor.poll(session.OutputOffset()); err != nil {
				stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				stopErr := provider.Stop(stopCtx, session)
				cancel()
				if stopErr != nil {
					return session, nil, errors.Join(err, stopErr)
				}
				output, _ := session.Result()
				return session, output, err
			}
		}
	}
}
