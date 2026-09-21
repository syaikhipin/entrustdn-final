// Command api runs the Thresh backend service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/config"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// version is the backend's version banner, stamped at build time or "dev".
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatalf("thresh-backend: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	log.Printf("thresh-backend %s: postgres connected", version)

	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}

	// Dev mail sink (stubbed, ticket 01): verification links will land in
	// the server log at pilot stage (spec: Membership;MAIL_SINK=log). The
	// mailsink package + its tests are the stub; registration wiring lands
	// with ticket 02.
	if cfg.DevMode {
		log.Printf("thresh-backend %s: dev mode on — verification links will go to the log sink", version)
	}

	handler := api.NewHandler(agentclient.New(cfg.AgentBaseURL), version)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("thresh-backend %s: listening on %s", version, cfg.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
