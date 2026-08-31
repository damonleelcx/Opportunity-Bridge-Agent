// Package config holds every runtime knob in one struct, loaded once at start.
//
// Nothing in this app reads an environment variable outside this file. That is
// what makes the stopping conditions, the anonymity floor and the rollout gate
// auditable: there is one place to look for what the running process believes.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr string

	// Model selection (step 4 of the build flow). See docs/04-model-choice.md.
	AgentModel      string
	ClassifierModel string
	Effort          string // default effort; an intent may raise it
	MaxTokens       int64

	// Stopping conditions (step 14). Per-intent caps in the intent registry are
	// clamped by these process-wide ones.
	MaxIterations   int
	MaxToolCalls    int
	MaxWallClock    time.Duration
	MaxOutputTokens int64
	MaxRetries      int

	// Guardrails.
	KAnonymityFloor int // smallest reportable cell in supply_demand_insight

	// Rollout gate (step 20). Intents not listed are visible but refuse to run,
	// with the reason shown, so a partial rollout is legible rather than silent.
	EnabledIntents []string

	// Storage. Empty means memory-only; the app still works, it just forgets.
	//
	// DatabaseURL, when set, is the durable home and StatePath is ignored. It is
	// the selector as well as the address: one variable decides the backend, so
	// there is no combination of settings whose meaning has to be worked out.
	// Which one is in use is logged at startup, because "I thought it was
	// writing to postgres" is the belief this must never leave intact.
	DatabaseURL   string
	DataDir       string
	StatePath     string
	CorpusDir     string
	TranscriptLog string

	// LLM backend selection. See backends.go for the per-provider table.
	Backend    Backend
	ScriptPath string
	APIKey     string

	// DeepSeekBaseURL allows pointing at a proxy or a regional endpoint.
	DeepSeekBaseURL string

	// Live lookup. The directory ships enabled and needs nothing. Web search is
	// what actually returns employers and courses nationwide, and it needs a
	// key — off by default rather than silently degrading.
	//
	// SearchProvider decides WHICH vendor, and it is an enum rather than
	// something inferred from the endpoint, because the vendors do not share a
	// wire shape: guessing wrong decodes cleanly and returns nothing, so the
	// failure would be silent. One key variable, one provider variable.
	SearchProvider  SearchProvider
	SearchAPIKey    string
	SearchAPIURL    string
	SearchKeyHeader string

	// External talent lookup, for the recruiter intent. Both OFF unless keyed,
	// like every other vendor seam here.
	//
	// These answer "how many people of this shape exist outside our opt-in
	// pool" - a market-size question. They never return a name, a contact or a
	// profile URL, from either vendor, even where the vendor supplies one. See
	// internal/talentsource for why that is the only coherent position.
	//
	// ‼️ PDLAPIURL exists mainly so it can be pointed at PDL's SANDBOX
	// (https://sandbox.api.peopledatalabs.com/v5/person/search), which answers
	// with SYNTHETIC records at zero credit cost. That is the right endpoint to
	// develop and demo against: it exercises the whole adapter without a real
	// person's data being read at all.
	PDLAPIKey    string
	PDLAPIURL    string
	ApolloAPIKey string
	ApolloAPIURL string

	// Read-aloud through a speech vendor. Off unless keyed, for the same reason
	// web search is: a feature that silently degrades is worse than one that
	// says it is not switched on. With no key the browser reads answers with its
	// own built-in voice, which is what it has always done.
	//
	// ‼️ TTSModel defaults to Fish's FREE backbone. Fish's terms for it say
	// requests may be used to improve model quality, and the text sent is the
	// answer — a city, an employment situation, sometimes a benefit being
	// claimed. Read docs/17-read-aloud.md before pointing this at real users.
	TTSAPIKey  string
	TTSVoiceID string
	TTSModel   string
	TTSAPIURL  string

	// Outgoing mail: confirm this address, and set a new password. OFF unless
	// SMTPHost is set, like every other vendor seam here — a deployment with no
	// relay still signs people up and still signs them in, it just cannot offer
	// a password reset, and it says so instead of showing a form that does
	// nothing.
	//
	// 🔴 PublicOrigin HAS NO DEFAULT, and mail stays off without it. Every link
	// in an outgoing message is absolute, and a guessed origin produces mail
	// that is either useless (a link to localhost) or dangerous (a link to
	// whatever host header the request happened to carry). Refusing to send is
	// the only honest answer to "we do not know where this service lives".
	//
	// SMTPFrom must be an address the relay credential is permitted to send as:
	// the relay runs with spoof protection, which maps a login to its own
	// address and the aliases pointing at it. SMTPReplyTo is separate on
	// purpose — the address a credential may SEND as and the mailbox a person
	// should REACH are different questions, and collapsing them into one
	// address means an alias that changes where inbound mail is delivered.
	// See docs/bugfix/2026-08-31-email-verification-and-reset.md
	PublicOrigin string
	SMTPHost     string
	SMTPPort     string
	SMTPFrom     string
	SMTPReplyTo  string
	SMTPUsername string
	SMTPPassword string

	// InviteCodes gate sign-up. EMPTY MEANS SIGN-UP IS CLOSED, not open: a
	// deployment that forgets to set them must refuse new accounts, never admit
	// everybody. See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
	InviteCodes []string
	// DemoAccount, if set and existing, adopts the subjects left behind by
	// visitors from before accounts existed. Empty skips the adoption.
	DemoAccount string
	// SignInTTL is how long a sign-in cookie stays valid.
	SignInTTL time.Duration

	// ReplyLanguage is the language the agent writes its answers in.
	// "zh-CN" | "en" | "match" (mirror whatever the person wrote in).
	// A session may override it; see prompt.ContextLayer.
	ReplyLanguage string

	// EnvFile records what the .env load did, for the startup log. It never
	// holds a value, only key names.
	EnvFile EnvFileResult

	// Warnings are non-fatal startup observations the caller should log. They
	// are collected rather than printed here so that config stays free of a
	// logger dependency and stays testable.
	Warnings []string
}

// ReplyLanguages are the accepted values for OBA_REPLY_LANGUAGE.
var ReplyLanguages = []string{"zh-CN", "en", "match"}

func Load() (Config, error) {
	// The .env file is read first, so everything below - including which backend
	// to use - can come from it. Real environment variables still win.
	envResult, err := LoadEnvFile(env("OBA_ENV_FILE", DefaultEnvFile))
	if err != nil {
		return Config{EnvFile: envResult}, err
	}

	backend := Backend(env("OBA_BACKEND", string(BackendAnthropic)))
	spec, knownBackend := backend.spec()

	// Model defaults follow the backend, so switching provider is one variable
	// rather than three that must be kept in step.
	c := Config{
		Addr:            env("OBA_ADDR", ":8787"),
		AgentModel:      env("OBA_AGENT_MODEL", spec.DefaultAgent),
		ClassifierModel: env("OBA_CLASSIFIER_MODEL", spec.DefaultClassifier),
		Effort:          env("OBA_EFFORT", "high"),
		MaxTokens:       int64(envInt("OBA_MAX_TOKENS", 16000)),
		MaxIterations:   envInt("OBA_MAX_ITERATIONS", 8),
		MaxToolCalls:    envInt("OBA_MAX_TOOL_CALLS", 16),
		MaxWallClock:    time.Duration(envInt("OBA_MAX_WALLCLOCK_SEC", 180)) * time.Second,
		MaxOutputTokens: int64(envInt("OBA_MAX_OUTPUT_TOKENS", 120000)),
		MaxRetries:      envInt("OBA_MAX_RETRIES", 2),
		KAnonymityFloor: envInt("OBA_K_ANONYMITY", 5),
		DatabaseURL:     env("OBA_DATABASE_URL", ""),
		DataDir:         env("OBA_DATA_DIR", "data"),
		StatePath:       env("OBA_STATE_PATH", ""),
		CorpusDir:       env("OBA_CORPUS_DIR", ""),
		TranscriptLog:   env("OBA_TRANSCRIPT_LOG", ""),
		Backend:         backend,
		ScriptPath:      env("OBA_SCRIPT", ""),
		DeepSeekBaseURL: env("OBA_DEEPSEEK_BASE_URL", ""),
		// Chinese by default: this serves people navigating Chinese public
		// services, and the surrounding prompt being written in English is an
		// artefact of the code, not a signal about who is reading the answer.
		ReplyLanguage:   env("OBA_REPLY_LANGUAGE", "zh-CN"),
		PublicOrigin:    strings.TrimRight(env("OBA_PUBLIC_ORIGIN", ""), "/"),
		SMTPHost:        env("OBA_SMTP_HOST", ""),
		SMTPPort:        env("OBA_SMTP_PORT", "587"),
		SMTPFrom:        env("OBA_SMTP_FROM", ""),
		SMTPReplyTo:     env("OBA_SMTP_REPLY_TO", ""),
		SMTPUsername:    env("OBA_SMTP_USERNAME", ""),
		SMTPPassword:    env("OBA_SMTP_PASSWORD", ""),
		SearchProvider:  SearchProvider(env("OBA_SEARCH_PROVIDER", string(SearchBocha))),
		SearchAPIKey:    env("OBA_SEARCH_API_KEY", ""),
		PDLAPIKey:       env("OBA_PDL_API_KEY", ""),
		PDLAPIURL:       env("OBA_PDL_API_URL", ""),
		ApolloAPIKey:    env("OBA_APOLLO_API_KEY", ""),
		ApolloAPIURL:    env("OBA_APOLLO_API_URL", ""),
		SearchAPIURL:    env("OBA_SEARCH_API_URL", ""),
		SearchKeyHeader: env("OBA_SEARCH_KEY_HEADER", ""),
		TTSAPIKey:       env("OBA_TTS_API_KEY", ""),
		TTSVoiceID:      env("OBA_TTS_VOICE_ID", ""),
		TTSModel:        env("OBA_TTS_MODEL", ""),
		TTSAPIURL:       env("OBA_TTS_API_URL", ""),
		InviteCodes:     splitList(env("OBA_INVITE_CODES", "")),
		DemoAccount:     env("OBA_DEMO_ACCOUNT", ""),
		SignInTTL:       time.Duration(envInt("OBA_SIGNIN_TTL_DAYS", 30)) * 24 * time.Hour,
		EnvFile:         envResult,
	}
	c.Warnings = append(c.Warnings, envResult.Warnings...)
	if !knownBackend {
		return c, fmt.Errorf("OBA_BACKEND=%q is not supported; expected one of: %s",
			backend, strings.Join(BackendNames(), ", "))
	}
	if spec.KeyEnv != "" {
		c.APIKey = os.Getenv(spec.KeyEnv)
	}
	for field, model := range map[string]string{
		"OBA_AGENT_MODEL":      c.AgentModel,
		"OBA_CLASSIFIER_MODEL": c.ClassifierModel,
	} {
		warn, err := checkModelBelongs(backend, field, model)
		if err != nil {
			return c, err
		}
		if warn != "" {
			c.Warnings = append(c.Warnings, warn)
		}
	}
	sort.Strings(c.Warnings)
	if c.CorpusDir == "" {
		c.CorpusDir = c.DataDir
	}
	if v := os.Getenv("OBA_ENABLED_INTENTS"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				c.EnabledIntents = append(c.EnabledIntents, p)
			}
		}
	}
	return c, c.Validate()
}

// MailConfigured reports whether this deployment can put a message in somebody's
// inbox. Every piece is required: a relay with no origin sends links nobody can
// follow, and an origin with no relay sends nothing at all. Reporting "on" while
// one of them is missing is how a password-reset form comes to look like it
// worked. See cmd/obagent/main.go, which logs which piece is absent.
func (c Config) MailConfigured() bool {
	return c.SMTPHost != "" && c.SMTPFrom != "" && c.PublicOrigin != ""
}

func (c Config) Validate() error {
	var errs []error
	if c.KAnonymityFloor < 2 {
		errs = append(errs, fmt.Errorf("OBA_K_ANONYMITY=%d: the anonymity floor must be at least 2; 1 would publish individuals", c.KAnonymityFloor))
	}
	if c.MaxIterations < 1 {
		errs = append(errs, errors.New("OBA_MAX_ITERATIONS must be at least 1"))
	}
	if c.MaxToolCalls < 1 {
		errs = append(errs, errors.New("OBA_MAX_TOOL_CALLS must be at least 1"))
	}
	valid := false
	for _, l := range ReplyLanguages {
		if c.ReplyLanguage == l {
			valid = true
		}
	}
	if !valid {
		errs = append(errs, fmt.Errorf("OBA_REPLY_LANGUAGE=%q: expected one of: %s",
			c.ReplyLanguage, strings.Join(ReplyLanguages, ", ")))
	}
	switch c.Effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		errs = append(errs, fmt.Errorf("OBA_EFFORT=%q: expected one of low, medium, high, xhigh, max", c.Effort))
	}
	spec, ok := c.Backend.spec()
	switch {
	case !ok:
		errs = append(errs, fmt.Errorf("OBA_BACKEND=%q: expected one of: %s",
			c.Backend, strings.Join(BackendNames(), ", ")))
	case c.Backend == BackendScripted && c.ScriptPath == "":
		errs = append(errs, errors.New("OBA_BACKEND=scripted requires OBA_SCRIPT=<path to a scripted-turn json file>"))
	case spec.RequiresKey && c.APIKey == "":
		// Refused at startup rather than on the first person's question: this
		// provider has no other credential source, so the failure is certain.
		errs = append(errs, fmt.Errorf("OBA_BACKEND=%s requires %s to be set; "+
			"create a key at platform.deepseek.com and export it", c.Backend, spec.KeyEnv))
	}
	// Checked even when no key is set. A misspelled provider name with the key
	// still to come is a deployment that would come up looking healthy and then
	// answer without live results; saying so at startup costs nothing.
	switch c.SearchProvider {
	case SearchBocha, SearchBrave:
	default:
		errs = append(errs, fmt.Errorf("OBA_SEARCH_PROVIDER=%q: expected one of: %s",
			c.SearchProvider, strings.Join(SearchProviderNames(), ", ")))
	}
	// A key with no voice would come up healthy and read every answer in the
	// vendor's default voice — which is the one thing the person configuring
	// this was trying to change. Refused rather than warned: it is certain to be
	// wrong, and it is one variable away from being right.
	if c.TTSAPIKey != "" && c.TTSVoiceID == "" {
		errs = append(errs, errors.New("OBA_TTS_API_KEY is set but OBA_TTS_VOICE_ID is empty: "+
			"without a voice id every answer is read in the vendor's default voice. "+
			"Set the model id from the voice's fish.audio URL, "+
			"for example OBA_TTS_VOICE_ID=df5c6c19dca944918dcbd6f1368fd02f"))
	}
	return errors.Join(errs...)
}

// IntentEnabled implements the rollout gate. An empty allowlist means every
// intent is on, which is the normal state; a non-empty one is a staged rollout.
func (c Config) IntentEnabled(id string) bool {
	if len(c.EnabledIntents) == 0 {
		return true
	}
	for _, e := range c.EnabledIntents {
		if e == id {
			return true
		}
	}
	return false
}

// splitList reads a comma-separated setting. Blank entries are dropped, so a
// trailing comma cannot become an empty invite code that matches an empty
// submission.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
