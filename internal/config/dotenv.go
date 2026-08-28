package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// DefaultEnvFile is where the loader looks unless OBA_ENV_FILE says otherwise.
const DefaultEnvFile = ".env"

// EnvFileResult reports what a load did, without ever reporting a value.
//
// Keys only, never values: this file holds an API key, and the first place a
// leaked secret usually turns up is a startup log somebody pasted into an issue.
type EnvFileResult struct {
	Path     string
	Found    bool
	SetKeys  []string // keys this file actually set
	Skipped  []string // keys already present in the real environment
	Warnings []string
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadEnvFile reads a .env file and puts its entries into the process
// environment.
//
// Two rules that matter more than the parsing:
//
//   - A real environment variable always wins. `DEEPSEEK_API_KEY=x make run`
//     must override the file, or a stale .env silently defeats every attempt to
//     run against something else.
//   - A missing file is not an error. Most deployments set real environment
//     variables and never have one.
func LoadEnvFile(path string) (EnvFileResult, error) {
	res := EnvFileResult{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("ENV_FILE_UNREADABLE: cannot stat %s: %w", path, err)
	}
	res.Found = true

	// A file holding an API key that the rest of the machine can read is worth
	// one line of warning. Not an error: on some systems, and inside many
	// containers, the mode is not the operator's to choose.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is readable by other users (mode %04o); it holds an API key. Consider: chmod 600 %s",
			path, info.Mode().Perm(), path))
	}

	f, err := os.Open(path)
	if err != nil {
		return res, fmt.Errorf("ENV_FILE_UNREADABLE: cannot open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		key, value, ok, err := parseEnvLine(sc.Text())
		if err != nil {
			return res, fmt.Errorf("ENV_FILE_INVALID: %s line %d: %w", path, line, err)
		}
		if !ok {
			continue
		}
		if _, present := os.LookupEnv(key); present {
			res.Skipped = append(res.Skipped, key)
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return res, fmt.Errorf("ENV_FILE_INVALID: %s line %d: cannot set %s: %w", path, line, key, err)
		}
		res.SetKeys = append(res.SetKeys, key)
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("ENV_FILE_UNREADABLE: %s: %w", path, err)
	}
	return res, nil
}

// parseEnvLine handles the shape people actually write:
//
//	# a comment
//	export KEY=value
//	KEY = "quoted, so # and = are literal"
//	KEY='single quoted, no escapes'
//	KEY=bare value   # trailing comment
//	KEY=
//
// It deliberately does NOT expand $OTHER. Expansion looks helpful and then
// silently mangles any secret containing a dollar sign.
func parseEnvLine(raw string) (key, value string, ok bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false, nil
	}
	s = strings.TrimPrefix(s, "export ")
	eq := strings.Index(s, "=")
	if eq < 0 {
		return "", "", false, fmt.Errorf("expected KEY=value, got %q", truncateLine(raw))
	}
	key = strings.TrimSpace(s[:eq])
	if !envKeyPattern.MatchString(key) {
		return "", "", false, fmt.Errorf("%q is not a valid variable name", key)
	}
	v := strings.TrimSpace(s[eq+1:])

	switch {
	case len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`):
		v = v[1 : len(v)-1]
		v = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\\`, `\`).Replace(v)
	case len(v) >= 2 && strings.HasPrefix(v, `'`) && strings.HasSuffix(v, `'`):
		v = v[1 : len(v)-1]
	default:
		// An unquoted value ends at a whitespace-preceded '#'. Quote the value
		// if it genuinely contains one.
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		v = strings.TrimSpace(v)
	}
	return key, v, true, nil
}

func truncateLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "…"
}
