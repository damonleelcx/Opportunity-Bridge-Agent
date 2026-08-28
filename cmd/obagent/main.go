// Command obagent runs the Opportunity Bridge Agent: the conversational
// interface, the HTTP API and the agent loop, in one process.
//
// Startup is deliberately fail-fast on anything that would make the agent
// dishonest rather than merely degraded. An unreadable corpus stops the process,
// because an agent with nothing to cite would spend the conversation improvising.
// An unreadable state file does not, because forgetting last week's session is
// not a reason to refuse today's.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

func main() {
	addr := flag.String("addr", "", "listen address (overrides OBA_ADDR)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(*addr, log); err != nil {
		log.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(addrOverride string, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("CONFIG_INVALID: %w", err)
	}
	if addrOverride != "" {
		cfg.Addr = addrOverride
	}

	c, err := corpus.Load(cfg.CorpusDir)
	if err != nil {
		return err
	}
	st := store.New(cfg.StatePath, log)
	seedSignals(st, cfg, log)
	adoptLegacyData(st, cfg, log)

	client, err := buildClient(cfg)
	if err != nil {
		return err
	}

	live, err := buildLiveSource(cfg, log)
	if err != nil {
		return err
	}
	ag := &agent.Agent{
		Cfg: cfg, LLM: client, Store: st, Corpus: c,
		Index: retrieval.NewIndex(c), Tools: toolsRegistry(), Live: live,
	}
	webFS, err := fs.Sub(web.Files, "static")
	if err != nil {
		return fmt.Errorf("WEB_ASSETS_MISSING: %w", err)
	}
	srv := &httpapi.Server{Agent: ag, Store: st, Cfg: cfg, Web: webFS, Log: log}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: a turn streams for as long as the agent's own
		// wall-clock budget allows, and that budget is the authority.
		IdleTimeout: 120 * time.Second,
	}

	if cfg.EnvFile.Found {
		// Key names only, never values.
		log.Info("loaded environment file", "path", cfg.EnvFile.Path,
			"set", cfg.EnvFile.SetKeys, "already_in_environment", cfg.EnvFile.Skipped)
	}
	log.Info("opportunity bridge agent ready",
		"addr", cfg.Addr, "backend", client.Name(), "reply_language", cfg.ReplyLanguage,
		"agent_model", cfg.AgentModel, "classifier_model", cfg.ClassifierModel,
		"opportunities", len(c.Opportunities), "knowledge_docs", len(c.Docs),
		"cities", c.Cities(), "k_anonymity_floor", cfg.KAnonymityFloor)
	for _, w := range cfg.Warnings {
		log.Warn(w, "code", "CONFIG_MODEL_UNRECOGNISED")
	}
	if len(cfg.EnabledIntents) > 0 {
		log.Warn("staged rollout is active: intents outside the allowlist will refuse with a visible reason",
			"enabled_intents", cfg.EnabledIntents)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("LISTEN_FAILED: could not serve on %s: %w", cfg.Addr, err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutCtx)
	}
}

// buildLiveSource assembles what the agent can consult beyond the corpus.
//
// The directory is a hard requirement: without it, a person in a city with no
// local listings has nowhere concrete to go, which is the gap this exists to
// close. Web search is added only when a key is present, and its absence is
// logged rather than hidden — a lookup that quietly does less is how "there is
// nothing in your city" becomes a lie.
func buildLiveSource(cfg config.Config, log *slog.Logger) (livesource.Chain, error) {
	dir, err := livesource.LoadDirectory(cfg.CorpusDir)
	if err != nil {
		return nil, err
	}
	chain := livesource.Chain{dir}
	if cfg.SearchAPIKey != "" {
		chain = append(chain, livesource.NewWebSearch(cfg.SearchAPIURL, cfg.SearchAPIKey, cfg.SearchKeyHeader))
		log.Info("live web search enabled", "regions_in_directory", dir.Regions())
	} else {
		log.Warn("live web search is OFF: no OBA_SEARCH_API_KEY. "+
			"Cities outside the corpus get the official directory and the national programmes, "+
			"but no current openings or courses",
			"code", "LIVE_SEARCH_DISABLED", "regions_in_directory", dir.Regions())
	}
	return chain, nil
}

func buildClient(cfg config.Config) (llm.Client, error) {
	switch cfg.Backend {
	case config.BackendScripted:
		return llm.LoadScript(cfg.ScriptPath)
	case config.BackendDeepSeek:
		return llm.NewDeepSeek(cfg.APIKey, cfg.DeepSeekBaseURL), nil
	case config.BackendAnthropic:
		return llm.NewAnthropic(cfg.APIKey), nil
	default:
		return nil, fmt.Errorf("BACKEND_UNSUPPORTED: %q; expected one of: %s",
			cfg.Backend, strings.Join(config.BackendNames(), ", "))
	}
}

// seedSignals loads the sample demand history on a fresh install so that gap
// analysis has something to aggregate. It never overwrites: once a deployment
// has its own signals, the samples stay out of the way.
func seedSignals(st *store.Store, cfg config.Config, log *slog.Logger) {
	if len(st.Signals()) > 0 {
		return
	}
	sigs, err := corpus.LoadSignals(cfg.CorpusDir)
	if err != nil {
		log.Warn("sample demand signals not loaded; gap analysis will start empty",
			"code", "SIGNAL_SEED_FAILED", "error", err)
		return
	}
	for _, s := range sigs {
		st.RecordSignal(s)
	}
	if len(sigs) > 0 {
		log.Info("seeded sample demand signals", "count", len(sigs))
	}
}

// adoptLegacyData gives the subjects left behind by visitors from before
// accounts existed to one named account, once.
//
// Why this is here rather than left alone: those records are real people's
// messages, and after the ownership checks went in they belong to nobody, which
// means nobody can read them, correct them or ask for them to be deleted. Giving
// them one owner restores every one of those. It adds, never deletes, and a
// marker in the store stops it running twice.
// See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
func adoptLegacyData(st *store.Store, cfg config.Config, log *slog.Logger) {
	if cfg.DemoAccount == "" {
		return
	}
	n, err := st.AdoptOrphanedSubjects(cfg.DemoAccount)
	if err != nil {
		// Not fatal: the service is fully functional without the adoption, and
		// refusing to start over a migration for historical data would take the
		// whole thing down for something nobody is waiting on. It is loud so it
		// is not missed.
		log.Warn("pre-account data was not adopted",
			"code", "LEGACY_ADOPTION_FAILED", "account", cfg.DemoAccount, "error", err)
		return
	}
	if n > 0 {
		log.Info("pre-account data adopted", "code", "LEGACY_ADOPTED",
			"account", cfg.DemoAccount, "subjects", n)
	}
}
