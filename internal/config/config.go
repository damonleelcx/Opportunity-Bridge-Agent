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
	SearchAPIKey    string
	SearchAPIURL    string
	SearchKeyHeader string

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
		SearchAPIKey:    env("OBA_SEARCH_API_KEY", ""),
		SearchAPIURL:    env("OBA_SEARCH_API_URL", ""),
		SearchKeyHeader: env("OBA_SEARCH_KEY_HEADER", ""),
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
