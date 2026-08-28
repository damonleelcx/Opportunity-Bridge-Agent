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

	client, err := buildClient(cfg)
	if err != nil {
		return err
	}

	ag := &agent.Agent{
		Cfg: cfg, LLM: client, Store: st, Corpus: c,
		Index: retrieval.NewIndex(c), Tools: toolsRegistry(),
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
