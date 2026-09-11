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
	"net"
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
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/talentsource"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tts"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

// teamCmd places an account in a firm, which is what makes a TEAM real in
// 猎源图谱: facts are shared across it, private judgements are not.
//
// An operator command rather than something an account can do to itself. A
// self-declared org is not an identity - type a competitor's name and you are
// inside their contact book - so the placement is made by whoever runs the
// deployment and has some other reason to believe it.
func teamCmd(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("team", flag.ContinueOnError)
	account := fs.String("account", "", "username to place")
	org := fs.String("org", "", "firm to place them in; empty removes them from one")
	list := fs.Bool("list", false, "list accounts and their firms")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := openStore(cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if *list {
		for _, a := range st.AllAccounts() {
			firm := a.Org
			if firm == "" {
				firm = "(team of one)"
			}
			fmt.Printf("%s\t%s\n", a.Username, firm)
		}
		return nil
	}
	if *account == "" {
		return errors.New("ACCOUNT_REQUIRED: -account names the username to place")
	}
	if err := st.SetAccountOrg(*account, *org); err != nil {
		return err
	}
	if *org == "" {
		fmt.Printf("%s is now a team of one\n", *account)
	} else {
		fmt.Printf("%s is now in %s\n", *account, *org)
	}
	return nil
}

func main() {
	// Operator subcommands come before the server's own flags: `obagent team`
	// is an administrative act, not a way to start the service.
	if len(os.Args) > 1 && os.Args[1] == "team" {
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		if err := teamCmd(log, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	addr := flag.String("addr", "", "listen address (overrides OBA_ADDR)")
	importState := flag.String("import-state", "",
		"one-time migration: copy this JSON state file into OBA_DATABASE_URL and exit. "+
			"Refuses if the database already holds records.")
	flag.Parse()

	// ContextHandler adds request_id (and run_id, for a message turn) to every line
	// logged with a request's context. See internal/obs/logsink.go.
	log := slog.New(obs.NewContextHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(log)

	if *importState != "" {
		if err := runImport(*importState, log); err != nil {
			log.Error("import failed", "error", err)
			os.Exit(1)
		}
		return
	}
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
	st, err := openStore(cfg, log)
	if err != nil {
		return err
	}
	defer st.Close()
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
	// 猎源图谱 shares this deployment's database and keeps its own tables. One
	// product, one process, one database - the separation that matters is the
	// import graph, not the schema, and a test asserts that one.
	//
	// Without a database there is no graph. It is NOT run in memory as a
	// fallback: that would look like a working feature and lose a recruiter's
	// whole book on the first restart. The tools say so when asked.
	var graph *leadgraph.Store
	if cfg.DatabaseURL != "" {
		gctx, gcancel := context.WithTimeout(context.Background(), 30*time.Second)
		graph, err = leadgraph.NewWithPostgres(gctx, cfg.DatabaseURL, log)
		gcancel()
		if err != nil {
			// LOUD, AND NOT FATAL. 猎源图谱 is a capability of one intent that
			// one role reaches. Refusing to start over it would let a
			// headhunting feature take down a public-service assistant - the
			// main path brought down by a branch off it, which is the exact
			// shape this project's architecture rules forbid.
			//
			// The tools already answer GRAPH_UNAVAILABLE with a remedy, so a
			// recruiter is told what is wrong instead of getting silence, and
			// every resident's conversation is unaffected.
			log.Error("猎源图谱 could not open; the agent is starting WITHOUT it",
				"code", "GRAPH_UNAVAILABLE", "error", err)
			graph = nil
		} else {
			defer graph.Close()
		}
	} else {
		log.Warn("no database configured: 猎源图谱 is unavailable this run",
			"code", "GRAPH_DISABLED")
	}

	ag := &agent.Agent{
		Cfg: cfg, LLM: client, Store: st, Corpus: c,
		Index: retrieval.NewIndex(c), Tools: toolsRegistry(), Live: live,
		Talent: buildTalentSource(cfg, log), Graph: graph, Log: log,
	}
	// 猎源图谱's daily pass. Every organisational change a recruiter recorded
	// should carry an alert, and 阿桥 tells them about it the next time they
	// talk. Nothing here can stop the service: it is a goroutine that logs.
	if graph != nil {
		stopDaily := startGraphDaily(st, graph, log)
		defer stopDaily()
	}
	webFS, err := fs.Sub(web.Files, "static")
	if err != nil {
		return fmt.Errorf("WEB_ASSETS_MISSING: %w", err)
	}
	srv := &httpapi.Server{Agent: ag, Store: st, Cfg: cfg, Web: webFS, Log: log,
		TTS: speechProvider(cfg, log), Mail: mailSender(cfg, log)}

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
	if cfg.SearchAPIKey == "" {
		log.Warn("live web search is OFF: no OBA_SEARCH_API_KEY. "+
			"Cities outside the corpus get the official directory and the national programmes, "+
			"but no current openings or courses",
			"code", "LIVE_SEARCH_DISABLED", "regions_in_directory", dir.Regions())
		return chain, nil
	}
	// The provider is chosen by name, never inferred from the endpoint: the two
	// vendors answer different shapes, and guessing wrong returns nothing rather
	// than failing. config.Validate has already rejected any other value.
	switch cfg.SearchProvider {
	case config.SearchBrave:
		chain = append(chain, livesource.NewWebSearch(cfg.SearchAPIURL, cfg.SearchAPIKey, cfg.SearchKeyHeader))
	default:
		chain = append(chain, livesource.NewBocha(cfg.SearchAPIURL, cfg.SearchAPIKey))
	}
	log.Info("live web search enabled",
		"provider", string(cfg.SearchProvider), "regions_in_directory", dir.Regions())
	return chain, nil
}

// buildTalentSource assembles the external people-index providers.
//
// Nil is returned when neither vendor is keyed, and that is the normal case. It
// is nil rather than an empty chain on purpose: external_talent_scan refuses
// outright when there is no provider, because an empty result would read as
// "nobody like that exists anywhere", which is the most misleading thing this
// feature could say. Nothing about the first-party pool depends on any of it.
func buildTalentSource(cfg config.Config, log *slog.Logger) talentsource.Provider {
	var chain talentsource.Chain
	if cfg.PDLAPIKey != "" {
		chain = append(chain, talentsource.NewPDL(cfg.PDLAPIURL, cfg.PDLAPIKey))
	}
	if cfg.ApolloAPIKey != "" {
		chain = append(chain, talentsource.NewApollo(cfg.ApolloAPIURL, cfg.ApolloAPIKey))
	}
	if len(chain) == 0 {
		log.Info("external talent scan is OFF: no OBA_PDL_API_KEY or OBA_APOLLO_API_KEY. "+
			"Recruiters see the opt-in pool only, and the size of the market outside it is reported as unknown",
			"code", "EXTERNAL_TALENT_DISABLED")
		return nil
	}
	names := make([]string, 0, len(chain))
	for _, p := range chain {
		names = append(names, p.Name())
	}
	log.Info("external talent scan enabled", "code", "EXTERNAL_TALENT_ENABLED", "vendors", names,
		"note", "market estimates only; no name, contact or profile link is returned by either vendor path")
	return chain
}

// speechProvider builds the read-aloud vendor, or nil when it is not configured.
//
// Off by default and loud about it, exactly like live web search above: a
// deployment with no key still reads answers aloud, in the browser's own voice,
// and the log says which of the two is happening. Silence about it would leave
// "we configured a voice and it is not being used" indistinguishable from "we
// never configured one".
func speechProvider(cfg config.Config, log *slog.Logger) tts.Provider {
	if cfg.TTSAPIKey == "" {
		log.Info("vendor read-aloud is OFF: no OBA_TTS_API_KEY. "+
			"Answers are still read aloud, using the browser's own built-in voice",
			"code", "TTS_DISABLED")
		return nil
	}
	p := tts.NewFish(cfg.TTSAPIURL, cfg.TTSAPIKey, cfg.TTSVoiceID, cfg.TTSModel, log)
	// The backbone is logged because one of them is free and the others bill,
	// and the difference is a header nobody sees. A deployment that has
	// accidentally switched to the paid model should be able to find that out
	// from its own startup line rather than from an invoice.
	log.Info("vendor read-aloud enabled",
		"provider", p.Name(), "voice_id", cfg.TTSVoiceID, "model", p.Model,
		"free_model", p.Model == tts.DefaultFishModel)
	return p
}

// runImport moves an existing JSON state file into postgres, once.
//
// It lives in the server binary rather than in a tool of its own because the
// deployed image is distroless and contains exactly one executable: a separate
// tool could not be run where the data actually is.
func runImport(path string, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("CONFIG_INVALID: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("IMPORT_NO_TARGET: -import-state needs OBA_DATABASE_URL set to the database to import into")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	counts, err := store.ImportFileToPostgres(ctx, path, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	log.Info("import complete", "code", "IMPORT_COMPLETE", "counts", counts.String())
	return nil
}

// openStore picks the durable home and says which one it picked.
//
// A deployment configured for postgres that cannot reach it does NOT fall back
// to a file. The fallback would be the more available choice and the wrong one:
// the service would come up, answer people, and write their records somewhere
// nobody is backing up, while the operator believed otherwise. Refusing to
// start is loud, and recoverable.
func openStore(cfg config.Config, log *slog.Logger) (*store.Store, error) {
	if cfg.DatabaseURL == "" {
		log.Info("state backend: json file", "code", "STATE_BACKEND",
			"path", cfg.StatePath, "durable", cfg.StatePath != "")
		return store.New(cfg.StatePath, log), nil
	}
	if cfg.StatePath != "" {
		// Both are set, which is what a deployment that switched to postgres
		// without clearing the old variable looks like. postgres wins, and the
		// ignored one is named so nobody goes looking in a file that stopped
		// being written.
		log.Warn("OBA_STATE_PATH is set and is being IGNORED: OBA_DATABASE_URL takes precedence",
			"code", "STATE_PATH_IGNORED", "ignored_path", cfg.StatePath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := store.OpenPostgres(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return nil, err
	}
	log.Info("state backend: postgres", "code", "STATE_BACKEND", "durable", true)
	return st, nil
}

func buildClient(cfg config.Config) (llm.Client, error) {
	switch cfg.Backend {
	case config.BackendScripted:
		return llm.LoadScript(cfg.ScriptPath)
	case config.BackendQwen:
		return llm.NewQwen(cfg.APIKey, cfg.QwenBaseURL), nil
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

// mailSender builds the outgoing-mail seam, or nil when this deployment has no
// relay.
//
// Off by default and LOUD about it, exactly like live web search and the speech
// vendor. The difference is what off costs: with no relay a person who forgets
// their password has no way back into an account holding their profile, their
// tracked tasks and their consents. So the warning names the missing piece
// rather than saying "mail is off", and /api/meta reports it so the interface
// can stop offering a reset form that cannot work.
//
// Every piece is required. A relay with no origin sends links that go nowhere;
// an origin with no relay sends nothing. Reporting configured while one is
// missing is how a reset form comes to look like it worked.
// See docs/bugfix/2026-08-31-email-verification-and-reset.md
func mailSender(cfg config.Config, log *slog.Logger) mailer.Sender {
	if !cfg.MailConfigured() {
		var missing []string
		for _, m := range []struct {
			name, value string
		}{
			{"OBA_SMTP_HOST", cfg.SMTPHost},
			{"OBA_SMTP_FROM", cfg.SMTPFrom},
			{"OBA_PUBLIC_ORIGIN", cfg.PublicOrigin},
		} {
			if m.value == "" {
				missing = append(missing, m.name)
			}
		}
		log.Warn("outgoing mail is OFF: address confirmation and password reset are unavailable. "+
			"Somebody who forgets their password will have no way back into their account",
			"code", "MAIL_DISABLED", "missing", strings.Join(missing, ", "))
		return nil
	}
	log.Info("outgoing mail enabled",
		"code", "MAIL_ENABLED", "relay", net.JoinHostPort(cfg.SMTPHost, cfg.SMTPPort),
		"from", cfg.SMTPFrom, "reply_to", cfg.SMTPReplyTo, "origin", cfg.PublicOrigin,
		"authenticated", cfg.SMTPUsername != "")
	return &mailer.SMTP{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		From: cfg.SMTPFrom, ReplyTo: cfg.SMTPReplyTo,
		Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
		Log: log,
	}
}

// startGraphDaily runs 猎源图谱's reconciliation pass on a timer and returns a
// function that stops it.
//
// WHY IN-PROCESS AND NOT A CRONJOB
//
//	The pass needs the same store and the same graph handle this process already
//	holds, and it writes nothing a second process could not corrupt but plenty a
//	second process would have to be configured for — a database URL, a firm
//	mapping, a schedule that has to be kept in step with a deployment. One
//	binary, one schedule, no second thing to install. If this ever needs to run
//	somewhere else, RunGraphDaily is already the whole of it.
//
// WHY IT RUNS ONCE SHORTLY AFTER START
//
//	A deployment that restarts daily would otherwise never reach the first tick.
//	The delay keeps it off the critical path of coming up.
func startGraphDaily(st *store.Store, graph *leadgraph.Store, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		first := time.NewTimer(2 * time.Minute)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		}
		for {
			// A pass is bounded: a hung one must not stop every later pass.
			pass, done := context.WithTimeout(ctx, 10*time.Minute)
			tools.RunGraphDaily(pass, st, graph, log, time.Now().UTC())
			done()
			t := time.NewTimer(tools.GraphDailyInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
	return cancel
}
