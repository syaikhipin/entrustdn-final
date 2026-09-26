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
	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/config"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
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

	// Object storage (ADR 0006): when configured, asset endpoints register;
	// otherwise the backend serves membership + credits only. A reachable
	// endpoint with a missing bucket is auto-created (dev convenience).
	var assetsDeps *api.AssetsDeps
	var taxonomyDeps *api.TaxonomyDeps
	var requestsDeps *api.RequestsDeps
	var assetSvc *assets.Service
	if cfg.S3.Enabled() {
		blobs, err := objectstore.NewS3(ctx, objectstore.S3Config{
			EndpointURL: cfg.S3.Endpoint,
			Region:      cfg.S3.Region,
			Bucket:      cfg.S3.Bucket,
			AccessKeyID: cfg.S3.AccessKeyID,
			SecretKey:   cfg.S3.SecretKey,
		})
		if err != nil {
			return err
		}
		if err := blobs.EnsureBucket(ctx); err != nil {
			return err
		}
		log.Printf("thresh-backend %s: object storage ready at %s (bucket %s)", version, cfg.S3.Endpoint, cfg.S3.Bucket)
		// The pseudonym map lives in Postgres beside the asset records —
		// in-platform only (ADR 0005), never in delivered data.
		pseudonyms := postgres.NewPseudonymMap(pool, cfg.PseudonymHashKey)

		// Taxonomy (ticket 06): seed the Irish vocabulary on first boot,
		// then let the ingest service classify against the taxonomy service
		// itself — it reloads the vocabulary per upload, so admin-created
		// terms and keyword edits reach the next ingest without a restart.
		taxStore := postgres.NewTaxonomyStore(pool)
		if err := postgres.SeedTaxonomyIfEmpty(ctx, pool); err != nil {
			return err
		}
		taxSvc := taxonomy.NewService(taxStore, postgres.NewAssetsStore(pool))
		taxonomyDeps = &api.TaxonomyDeps{
			Service: taxSvc,
			Rules:   postgres.CreditRulesLoader(pool),
		}

		assetSvc = assets.NewService(postgres.NewAssetsStore(pool), blobs, assets.NewPipeline(anonymize.NewStage(pseudonyms))).
			WithCategorizer(taxSvc)
		assetsDeps = &api.AssetsDeps{
			Service:    assetSvc,
			Pseudonyms: &api.PseudonymDeps{Map: pseudonyms},
		}
	} else {
		log.Printf("thresh-backend %s: S3_ENDPOINT_URL not set — asset endpoints disabled", version)
	}

	// Module registry (ticket 08): DB-only — no object storage dependency.
	// Any active account may upload; the Platform Admin promotes. Built
	// before the requests deps so ticket 13's attach endpoints can authorize
	// through it.
	modulesSvc := modules.NewService(postgres.NewModulesStore(pool))

	// Requests & clarification (ticket 07): every chat turn hands the agent
	// a live catalog snapshot — the assets service's catalog at the current
	// price book's cached-download price. Without object storage there is
	// no catalog to check, so the endpoints don't register.
	if assetSvc != nil {
		rulesLoader := postgres.CreditRulesLoader(pool)
		requestsDeps = &api.RequestsDeps{
			Store: postgres.NewRequestsStore(pool),
			// Ticket 13: the registry authorizes every template/skill attach.
			Modules: modulesSvc,
			Catalog: func(ctx context.Context) ([]contract.CatalogAsset, error) {
				rules, err := rulesLoader(ctx)
				if err != nil {
					return nil, err
				}
				entries, _, err := assetSvc.Catalog(ctx, rules.Data.CachedAssetMicrosPerUnit)
				if err != nil {
					return nil, err
				}
				out := make([]contract.CatalogAsset, 0, len(entries))
				for _, e := range entries {
					stamps := make([]contract.CategoryStamp, 0, len(e.Categories))
					for _, a := range e.Categories {
						stamps = append(stamps, contract.CategoryStamp{
							Category: string(a.Category), Value: a.Value, Label: a.Label,
						})
					}
					out = append(out, contract.CatalogAsset{
						ID: e.ID, Name: e.Name, Description: e.Description,
						Categories:        stamps,
						CachedPriceMicros: e.CachedPriceMicros,
					})
				}
				return out, nil
			},
		}
	}

	// Data Collections (ticket 12): re-asks open through the same
	// conversations service shape the endpoints use, the delivery is
	// cleaned through the pseudonym map's Delivery (ADR 0005) and charged
	// through the Ledger. Pseudonyms ride the Postgres map even without S3
	// — collections are DB-only.
	agentClient := agentclient.New(cfg.AgentBaseURL)
	pseudonymMap := postgres.NewPseudonymMap(pool, cfg.PseudonymHashKey)
	convStore := postgres.NewConversationsStore(pool)
	rosterStore := postgres.NewRosterStore(pool)
	requestsStore := postgres.NewRequestsStore(pool)
	creditsSvc := credits.NewService(postgres.NewCreditsStore(pool), postgres.CreditRulesLoader(pool))
	convSvc := conversations.NewService(convStore,
		api.AgentConversationAdapter{Client: agentClient}, cfg.PublicBaseURL)
	colSvc := collections.NewService(collections.Deps{
		Store:         postgres.NewCollectionsStore(pool),
		Conversations: convStore,
		Opener:        convSvc,
		Roster:        rosterStore,
		Requests:      requestsStore,
	}).WithCredits(creditsSvc).
		WithCleaner(anonymize.NewDelivery(pseudonymMap)).
		WithPseudonyms(pseudonymMap)

	// Member roster & conversations (ticket 11): DB-only like the modules —
	// channels belong to the agent sidecar, not this process. Resumable
	// links carry the deployment's public URL when one is configured.
	handler := api.NewHandler(api.Deps{
		Agent:   agentClient,
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
		Assets:   assetsDeps,
		Taxonomy: taxonomyDeps,
		Requests: requestsDeps,
		Modules:  &api.ModulesDeps{Service: modulesSvc},
		Roster:   &api.RosterDeps{Store: rosterStore},
		Conversations: &api.ConversationsDeps{
			Store:         convStore,
			PublicBaseURL: cfg.PublicBaseURL,
		},
		Collections: &api.CollectionsDeps{Service: colSvc},
		Earnings: &api.EarningsDeps{
			Store:  postgres.NewCreditsStore(pool),
			Roster: rosterStore,
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
