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
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
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

	// Dev mail sink: verification links land in the server log at pilot
	// stage (spec: Membership; MAIL_SINK=log). An SMTP sink slots in behind
	// the same interface when the pilot gets a mail server.
	var mail mailsink.Sink = mailsink.NewLogSink(os.Stderr)
	if cfg.DevMode {
		log.Printf("thresh-backend %s: dev mode on — verification links go to the log sink", version)
	}

	store := postgres.NewStore(pool)

	// Seed the first Platform Admin and initial TOS version (idempotent).
	if err := runBootstrap(ctx, store, mail, bootstrapConfig{
		AdminEmail:    cfg.BootstrapAdminEmail,
		AdminPassword: cfg.BootstrapAdminPassword,
		TOSVersion:    cfg.BootstrapTOSVersion,
		TOSBody:       cfg.BootstrapTOSBody,
	}); err != nil {
		return err
	}

	handler := api.NewHandler(api.Deps{
		Agent:   agentclient.New(cfg.AgentBaseURL),
		Version: version,
		Store:   store,
		Mail:    mail,
		Credits: &api.CreditsDeps{
			Store: postgres.NewCreditsStore(pool),
			Rules: postgres.CreditRulesLoader(pool),
			SaveRules: func(ctx context.Context, rules credits.PricingRules) error {
				return postgres.SaveCreditRules(ctx, pool, rules)
			},
		},
	})
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
