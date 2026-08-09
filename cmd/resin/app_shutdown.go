package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

const initializationCleanupTimeout = 5 * time.Second

type shutdownStep struct {
	name string
	skip bool
	run  func(context.Context) error
}

func (a *resinApp) shutdown(ctx context.Context) error {
	return runShutdownSteps(ctx, a.shutdownSteps())
}

func cleanupFailedInitialization(app *resinApp, initErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), initializationCleanupTimeout)
	defer cancel()
	return errors.Join(initErr, app.shutdown(ctx))
}

func runShutdownSteps(ctx context.Context, steps []shutdownStep) error {
	var errs []error
	for _, step := range steps {
		if step.skip {
			continue
		}
		if err := step.run(ctx); err != nil {
			wrapped := fmt.Errorf("%s: %w", step.name, err)
			errs = append(errs, wrapped)
			log.Printf("%s shutdown failed: %v", step.name, err)
			continue
		}
		log.Printf("%s stopped", step.name)
	}
	return errors.Join(errs...)
}

func optionalShutdownStep(name string, active bool, run func(context.Context) error) shutdownStep {
	return shutdownStep{name: name, skip: !active, run: run}
}

func (a *resinApp) shutdownSteps() []shutdownStep {
	steps := []shutdownStep{a.endpointShutdownStep()}
	steps = append(steps, a.eventSourceShutdownSteps()...)
	steps = append(steps, a.outboundShutdownSteps()...)
	steps = append(steps, a.observabilityShutdownSteps()...)
	return append(steps, a.persistenceShutdownStep())
}

func (a *resinApp) endpointShutdownStep() shutdownStep {
	return optionalShutdownStep("endpoints", a.endpointManager != nil, func(ctx context.Context) error {
		return a.endpointManager.Shutdown(ctx)
	})
}

func (a *resinApp) eventSourceShutdownSteps() []shutdownStep {
	return []shutdownStep{
		optionalShutdownStep("lease cleaner", a.topoRuntime != nil && a.topoRuntime.leaseCleaner != nil, func(context.Context) error {
			a.topoRuntime.leaseCleaner.Stop()
			return nil
		}),
		optionalShutdownStep("ephemeral cleaner", a.topoRuntime != nil && a.topoRuntime.ephemeralCleaner != nil, func(context.Context) error {
			a.topoRuntime.ephemeralCleaner.Stop()
			return nil
		}),
		optionalShutdownStep("subscription scheduler", a.topoRuntime != nil && a.topoRuntime.scheduler != nil, func(context.Context) error {
			a.topoRuntime.scheduler.Stop()
			return nil
		}),
		optionalShutdownStep("probe manager", a.topoRuntime != nil && a.topoRuntime.probeMgr != nil, func(context.Context) error {
			a.topoRuntime.probeMgr.Stop()
			return nil
		}),
		optionalShutdownStep("GeoIP service", a.geoSvc != nil, func(context.Context) error {
			a.geoSvc.Stop()
			return nil
		}),
	}
}

func (a *resinApp) outboundShutdownSteps() []shutdownStep {
	return []shutdownStep{
		optionalShutdownStep("outbound transport pool", a.transportPool != nil, func(context.Context) error {
			a.transportPool.CloseAll()
			return nil
		}),
		optionalShutdownStep("outbound manager", a.topoRuntime != nil && a.topoRuntime.outboundMgr != nil, func(context.Context) error {
			return a.topoRuntime.outboundMgr.Close()
		}),
		optionalShutdownStep("sing-box services", a.topoRuntime != nil && a.topoRuntime.singboxBuilder != nil, func(context.Context) error {
			return a.topoRuntime.singboxBuilder.Close()
		}),
	}
}

func (a *resinApp) observabilityShutdownSteps() []shutdownStep {
	return []shutdownStep{
		optionalShutdownStep("request log service", a.requestlogSvc != nil, func(context.Context) error {
			a.requestlogSvc.Stop()
			return nil
		}),
		optionalShutdownStep("request log repo", a.requestlogRepo != nil, func(context.Context) error {
			return a.requestlogRepo.Close()
		}),
		optionalShutdownStep("metrics manager", a.metricsManager != nil, func(context.Context) error {
			a.metricsManager.Stop()
			return nil
		}),
		optionalShutdownStep("metrics DB", a.metricsDB != nil, func(context.Context) error {
			return a.metricsDB.Close()
		}),
	}
}

func (a *resinApp) persistenceShutdownStep() shutdownStep {
	return optionalShutdownStep("cache flush worker", a.flushWorker != nil, func(context.Context) error {
		a.flushWorker.Stop()
		return nil
	})
}
