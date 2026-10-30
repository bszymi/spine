package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bszymi/spine/service/internal/gateway"
)

// runServer starts the long-lived background services bundled in the
// serveRuntime, listens on the configured port, and waits for either
// the listener to fail or a SIGINT/SIGTERM. On either path it tears
// down the scheduler, the delivery context, and finally the HTTP server
// with a 10s shutdown deadline.
//
// Split out of serveCmd so the serve command keeps its config-and-build
// concerns out of the lifecycle plumbing — and so a future smoke test
// or alternative entrypoint can reuse the lifecycle without copying it.
func runServer(ctx context.Context, srv *gateway.Server, rt *serveRuntime, log *slog.Logger, port string) error {
	startBackgroundServices(ctx, rt, log)

	listenErr := make(chan error, 1)
	go func() {
		log.Info("spine server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-listenErr:
		return fmt.Errorf("server failed to start: %w", err)
	case <-sig:
	}

	log.Info("spine server shutting down")
	stopBackgroundServices(rt)
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	drainBackgroundServices(shutdownCtx, rt, log)
	return err
}

// startBackgroundServices runs startup recovery and starts the scheduler
// of the mode (single: Scheduler; shared: MultiScheduler, INIT-024
// TASK-008) and the projection sync loops, all under ctx.
func startBackgroundServices(ctx context.Context, rt *serveRuntime, log *slog.Logger) {
	if rt.Scheduler != nil {
		if result, err := rt.Scheduler.RecoverOnStartup(ctx); err != nil {
			log.Error("startup recovery failed", "error", err)
		} else {
			log.Info("startup recovery complete",
				"pending_activated", result.PendingActivated,
				"active_resumed", result.ActiveResumed,
			)
		}
		go rt.Scheduler.Start(ctx)
	}
	if rt.MultiScheduler != nil {
		go rt.MultiScheduler.Start(ctx)
	}
	if rt.ProjSync != nil {
		go rt.ProjSync.StartSyncLoop(ctx)
	}
	if rt.MultiProjSync != nil {
		go rt.MultiProjSync.Start(ctx)
	}
}

// stopBackgroundServices signals every background service to stop. None
// of them is waited for here.
func stopBackgroundServices(rt *serveRuntime) {
	if rt.DeliveryCancel != nil {
		rt.DeliveryCancel()
	}
	if rt.Scheduler != nil {
		rt.Scheduler.Stop()
	}
	if rt.MultiScheduler != nil {
		rt.MultiScheduler.Stop()
	}
	if rt.MultiProjSync != nil {
		rt.MultiProjSync.Stop()
	}
}

// drainBackgroundServices waits, until ctx is done, for the shared-mode
// scheduler's workspace scans still running, so the workspace pool
// serveCmd closes next is not closed under them (waitMultiScheduler).
// The single-workspace scheduler is not waited for, as before.
func drainBackgroundServices(ctx context.Context, rt *serveRuntime, log *slog.Logger) {
	waitMultiScheduler(ctx, rt.MultiScheduler, log)
}
