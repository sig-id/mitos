package studio

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mitos/internal/compute"
	"mitos/internal/resourcepolicy"
)

// compileApplication admits an application's declared compute request
// against every configured provider. Applications without a request use the
// local development exception; a declared confined posture is admitted only by
// an enforcing provider and is never downgraded to the local process.
func (e *Engine) compileApplication(app Application) (compute.LaunchSpec, error) {
	request := compute.Request{}
	development := app.Compute == nil
	if app.Compute != nil {
		request = *app.Compute
	}
	providers := map[string]compute.Capabilities{"local": (&compute.Local{}).Capabilities()}
	if e != nil && e.nucleus != nil {
		providers[compute.ConfinementNucleus] = e.nucleus.Capabilities()
	}
	return compute.Compile(request, providers, development)
}

// providerFor returns the provider a frozen launch was admitted against. A
// launch admitted by a provider that is no longer configured is refused rather
// than run under a weaker one.
func (e *Engine) providerFor(launch compute.LaunchSpec) (compute.Provider, error) {
	switch launch.Provider {
	case "", "local":
		if e.provider == nil {
			return nil, errors.New("execution provider is not configured")
		}
		return e.provider, nil
	case compute.ConfinementNucleus:
		if e.nucleus == nil {
			return nil, errors.New("this launch was admitted by the nucleus provider, which is not configured; set MITOS_NUCLEUS")
		}
		return e.nucleus, nil
	}
	return nil, fmt.Errorf("launch provider %q is not available", launch.Provider)
}

// Automatic workflows use native local agents, while preserving their frozen
// per-agent resource request through the same provider admission contract.
func (e *Engine) providerCapabilities() compute.Capabilities { return e.provider.Capabilities() }

// agentProviderCapabilities reports the posture native agents are admitted
// against: the confining provider when configured, the plain local provider
// otherwise.
func (e *Engine) agentProviderCapabilities() compute.Capabilities {
	return e.agentProvider.Capabilities()
}

// compileWorkflowLaunch keeps the broker/database-free local startup. Under a
// confining provider the launch admits the container posture the provider can
// actually enforce instead of the development exception.
func compileWorkflowLaunch(resources resourcepolicy.Agent, caps compute.Capabilities, development bool) (compute.LaunchSpec, error) {
	request := compute.Request{Resources: resources}
	if !development {
		request.NetMode = compute.NetOff
		if len(caps.NetModes) == 1 {
			request.NetMode = caps.NetModes[0]
		}
	}
	return compute.Compile(request, map[string]compute.Capabilities{"local": caps}, development)
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
