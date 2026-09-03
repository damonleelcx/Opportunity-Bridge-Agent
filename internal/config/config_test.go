package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// Switching provider without changing the model ids is the obvious mistake, and
// it fails in the worst way: a compatibility layer maps the unrecognised id onto
// its own default, so the process keeps working while answering from a model
// nobody chose. These tests hold the startup refusal that prevents that.

func TestBackendDefaultsFollowTheBackend(t *testing.T) {
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "qw-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AgentModel != llm.QwenAgentModel || cfg.ClassifierModel != llm.QwenClassifierModel {
		t.Errorf("models did not follow the backend: agent=%q classifier=%q",
			cfg.AgentModel, cfg.ClassifierModel)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

// TestRetiredProviderModelIsRefusedAtStartup: the message has to say what to
// change, not merely that something is wrong. Somebody meets this immediately
// after an upgrade, having changed nothing themselves.
func TestRetiredProviderModelIsRefusedAtStartup(t *testing.T) {
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "qw-test")
	t.Setenv("OBA_AGENT_MODEL", "claude-opus-5")
	_, err := config.Load()
	if err == nil {
		t.Fatal("a Claude model on the Qwen backend was accepted")
	}
	for _, want := range []string{"claude-opus-5", "anthropic", "OBA_AGENT_MODEL", llm.QwenAgentModel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

func TestUnknownModelWarnsButProceeds(t *testing.T) {
	// A proxy, a marketplace id, or a model released after this build is
	// legitimate. It is logged, not blocked.
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "qw-test")
	t.Setenv("OBA_AGENT_MODEL", "qwen4.0-something")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("an unrecognised id of the right family was blocked: %v", err)
	}
	if len(cfg.Warnings) == 0 {
		t.Error("an unrecognised model id passed with no warning at all")
	}
}

// TestQwenRequiresItsKey. Unlike the Anthropic backend this replaced, there is no
// OAuth or ambient-credential path here, so an empty variable IS proof of no
// credential and the failure is certain. Refused at startup rather than on the
// first person's question.
func TestQwenRequiresItsKey(t *testing.T) {
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "QWEN_API_KEY") {
		t.Fatalf("a missing Qwen key was not refused at startup: %v", err)
	}
}

// TestKeyErrorNamesTheRegionalTrap: a Beijing key 401s on the Singapore host
// exactly as a revoked key does, so the startup message is the one place a
// reader can be warned before they go looking for a key that is already fine.
func TestKeyErrorNamesTheRegionalTrap(t *testing.T) {
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "REGIONAL") {
		t.Fatalf("the missing-key error does not mention that the key is regional: %v", err)
	}
}

func TestUnknownBackendNamesTheAlternatives(t *testing.T) {
	t.Setenv("OBA_BACKEND", "gpt")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"qwen", "scripted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not list %q as an option:\n%s", want, err)
		}
	}
}

func TestAnonymityFloorCannotBeDisabled(t *testing.T) {
	t.Setenv("OBA_K_ANONYMITY", "1")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "publish individuals") {
		t.Fatalf("a floor of 1 was accepted: %v", err)
	}
}

// ── .env ───────────────────────────────────────────────────────────────────

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnvFileParsesWhatPeopleActuallyWrite(t *testing.T) {
	path := writeEnv(t, strings.Join([]string{
		"# a comment",
		"",
		"export OBA_TEST_PLAIN=hello",
		"OBA_TEST_SPACED = spaced out ",
		`OBA_TEST_QUOTED="has = and # inside"`,
		"OBA_TEST_SINGLE='literal \\n stays'",
		"OBA_TEST_TRAILING=value # trailing comment",
		"OBA_TEST_EMPTY=",
		`OBA_TEST_ESCAPES="line1\nline2"`,
		"OBA_TEST_DOLLAR=abc$NOT_EXPANDED",
	}, "\n"))
	for _, k := range []string{"OBA_TEST_PLAIN", "OBA_TEST_SPACED", "OBA_TEST_QUOTED",
		"OBA_TEST_SINGLE", "OBA_TEST_TRAILING", "OBA_TEST_EMPTY", "OBA_TEST_ESCAPES", "OBA_TEST_DOLLAR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	res, err := config.LoadEnvFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !res.Found || len(res.SetKeys) != 8 {
		t.Fatalf("set %d keys: %v", len(res.SetKeys), res.SetKeys)
	}
	for _, tc := range []struct{ key, want string }{
		{"OBA_TEST_PLAIN", "hello"},
		{"OBA_TEST_SPACED", "spaced out"},
		{"OBA_TEST_QUOTED", "has = and # inside"},
		{"OBA_TEST_SINGLE", `literal \n stays`},
		{"OBA_TEST_TRAILING", "value"},
		{"OBA_TEST_EMPTY", ""},
		{"OBA_TEST_ESCAPES", "line1\nline2"},
		// Expansion looks helpful and then silently mangles any secret with a
		// dollar sign in it, so it is deliberately not done.
		{"OBA_TEST_DOLLAR", "abc$NOT_EXPANDED"},
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// The rule that keeps a stale .env from defeating every attempt to run against
// something else.
func TestRealEnvironmentBeatsTheFile(t *testing.T) {
	path := writeEnv(t, "OBA_TEST_WINS=from-file\n")
	t.Setenv("OBA_TEST_WINS", "from-environment")
	res, err := config.LoadEnvFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("OBA_TEST_WINS"); got != "from-environment" {
		t.Errorf("the file overrode a real variable: %q", got)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "OBA_TEST_WINS" {
		t.Errorf("the skip was not reported: %+v", res.Skipped)
	}
}

func TestMissingEnvFileIsNotAnError(t *testing.T) {
	res, err := config.LoadEnvFile(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil || res.Found {
		t.Fatalf("a missing file was treated as a problem: %v %+v", err, res)
	}
}

func TestEnvFileReportsKeysNeverValues(t *testing.T) {
	// A leaked secret's usual first appearance is a startup log somebody pasted
	// into an issue, so the result type must not be able to carry a value.
	path := writeEnv(t, "OBA_TEST_SECRET=sk-super-secret-value\n")
	os.Unsetenv("OBA_TEST_SECRET")
	res, err := config.LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", res), "sk-super-secret-value") {
		t.Fatal("the load result carries the secret value")
	}
}

func TestEnvFileErrorsNameTheLine(t *testing.T) {
	path := writeEnv(t, "GOOD=1\nthis line has no equals sign\n")
	_, err := config.LoadEnvFile(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("the error does not point at the line: %v", err)
	}
}

func TestLoosePermissionsWarnButDoNotBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not the same idea on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("OBA_TEST_PERM=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := config.LoadEnvFile(path)
	if err != nil {
		t.Fatalf("a readable file was refused outright: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("a world-readable file holding an API key produced no warning")
	}
}

func TestConfigReadsTheKeyFromTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path,
		[]byte("OBA_BACKEND=qwen\nQWEN_API_KEY=sk-from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBA_ENV_FILE", path)
	os.Unsetenv("OBA_BACKEND")
	os.Unsetenv("QWEN_API_KEY")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Backend != config.BackendQwen || cfg.APIKey != "sk-from-dotenv" {
		t.Errorf("the file did not reach the config: backend=%q key set=%v",
			cfg.Backend, cfg.APIKey != "")
	}
	if cfg.AgentModel != llm.QwenAgentModel {
		t.Errorf("model defaults did not follow the backend chosen in the file: %q", cfg.AgentModel)
	}
}

// TestDefaultBackendIsQwen pins the default provider. It is a one-line check over
// a decision that is otherwise invisible: nothing else in the tree states which
// provider a deployment with an empty .env will actually call.
func TestDefaultBackendIsQwen(t *testing.T) {
	os.Unsetenv("OBA_BACKEND")
	t.Setenv("QWEN_API_KEY", "sk-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Backend != config.BackendQwen {
		t.Errorf("default backend = %q, want qwen", cfg.Backend)
	}
	if cfg.AgentModel != llm.QwenAgentModel || cfg.ClassifierModel != llm.QwenClassifierModel {
		t.Errorf("default models = %q / %q, want %q / %q",
			cfg.AgentModel, cfg.ClassifierModel, llm.QwenAgentModel, llm.QwenClassifierModel)
	}
}

// TestRetiredModelIdsAreRefused is the upgrade fence.
//
// A deployment upgrading in place keeps its .env, which very likely still names
// a model from the Anthropic or DeepSeek backends this build removed. The
// DeepSeek ids are the dangerous ones: Model Studio is a multi-vendor
// marketplace and genuinely answers `deepseek-v4-pro` with HTTP 200, so without
// this check the process comes up healthy and bills for a model nobody chose in
// the new configuration.
func TestRetiredModelIdsAreRefused(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "deepseek-v4-pro", "deepseek-v4-flash"} {
		t.Setenv("QWEN_API_KEY", "sk-test")
		t.Setenv("OBA_BACKEND", "qwen")
		t.Setenv("OBA_AGENT_MODEL", model)
		_, err := config.Load()
		if err == nil {
			t.Errorf("OBA_AGENT_MODEL=%s was accepted; a leftover id from a removed backend must "+
				"not come up looking healthy", model)
			continue
		}
		if !strings.Contains(err.Error(), model) {
			t.Errorf("the error for %s does not name the model: %v", model, err)
		}
	}
}

// TestMarketplaceModelIdsAreAllowed: only OUR OWN retired defaults are refused.
// Model Studio serves third-party ids too, and someone may deliberately want one,
// so an unrecognised id is a warning rather than a refusal.
func TestMarketplaceModelIdsAreAllowed(t *testing.T) {
	t.Setenv("QWEN_API_KEY", "sk-test")
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("OBA_AGENT_MODEL", "ZHIPU/GLM-5.3-Flash")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("a third-party marketplace id was refused outright: %v", err)
	}
	if len(cfg.Warnings) == 0 {
		t.Error("an unrecognised model id produced no warning at all")
	}
}

// ── reply language ─────────────────────────────────────────────────────────

func TestReplyLanguageDefaultsToChinese(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReplyLanguage != "zh-CN" {
		t.Errorf("default reply language %q; this serves people navigating Chinese public services", cfg.ReplyLanguage)
	}
}

func TestReplyLanguageIsValidated(t *testing.T) {
	t.Setenv("OBA_REPLY_LANGUAGE", "klingon")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "zh-CN") {
		t.Fatalf("an unsupported language was accepted, or the error did not list the options: %v", err)
	}
}
