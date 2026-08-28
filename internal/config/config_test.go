package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
)

// Switching provider without changing the model ids is the obvious mistake, and
// it fails in the worst way: a compatibility layer maps the unrecognised id onto
// its own default, so the process keeps working while answering from a model
// nobody chose. These tests hold the startup refusal that prevents that.

func TestBackendDefaultsFollowTheBackend(t *testing.T) {
	t.Setenv("OBA_BACKEND", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "ds-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AgentModel != "deepseek-v4-pro" || cfg.ClassifierModel != "deepseek-v4-flash" {
		t.Errorf("models did not follow the backend: agent=%q classifier=%q",
			cfg.AgentModel, cfg.ClassifierModel)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestCrossProviderModelIsRefusedAtStartup(t *testing.T) {
	t.Setenv("OBA_BACKEND", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "ds-test")
	t.Setenv("OBA_AGENT_MODEL", "claude-opus-5")
	_, err := config.Load()
	if err == nil {
		t.Fatal("a Claude model on the DeepSeek backend was accepted")
	}
	// The message has to say what to change, not just that something is wrong.
	for _, want := range []string{"claude-opus-5", "anthropic backend", "OBA_BACKEND=deepseek", "deepseek-v4-pro"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

func TestUnknownModelWarnsButProceeds(t *testing.T) {
	// A proxy or a model released after this build is legitimate. It is logged,
	// not blocked.
	t.Setenv("OBA_BACKEND", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "ds-test")
	t.Setenv("OBA_AGENT_MODEL", "deepseek-v5-something")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("an unrecognised id of the right family was blocked: %v", err)
	}
	if len(cfg.Warnings) == 0 {
		t.Error("an unrecognised model id passed with no warning at all")
	}
}

func TestDeepSeekRequiresItsKey(t *testing.T) {
	t.Setenv("OBA_BACKEND", "deepseek")
	t.Setenv("DEEPSEEK_API_KEY", "")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("a missing DeepSeek key was not refused at startup: %v", err)
	}
}

func TestAnthropicDoesNotRequireAnEnvKey(t *testing.T) {
	// The SDK also resolves an OAuth profile, so an empty variable is not proof
	// of no credential; refusing here would break a legitimate setup.
	t.Setenv("OBA_BACKEND", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := config.Load(); err != nil {
		t.Fatalf("anthropic with no env key was refused: %v", err)
	}
}

func TestUnknownBackendNamesTheAlternatives(t *testing.T) {
	t.Setenv("OBA_BACKEND", "gpt")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"anthropic", "deepseek", "scripted"} {
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
		[]byte("OBA_BACKEND=deepseek\nDEEPSEEK_API_KEY=sk-from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBA_ENV_FILE", path)
	os.Unsetenv("OBA_BACKEND")
	os.Unsetenv("DEEPSEEK_API_KEY")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Backend != config.BackendDeepSeek || cfg.APIKey != "sk-from-dotenv" {
		t.Errorf("the file did not reach the config: backend=%q key set=%v",
			cfg.Backend, cfg.APIKey != "")
	}
	if cfg.AgentModel != "deepseek-v4-pro" {
		t.Errorf("model defaults did not follow the backend chosen in the file: %q", cfg.AgentModel)
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
