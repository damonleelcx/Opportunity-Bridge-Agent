package config

import (
	"fmt"
	"sort"
	"strings"
)

// Backend names the model provider this process talks to.
type Backend string

const (
	// BackendAnthropic is the Claude API (or anything speaking its wire format
	// at ANTHROPIC_BASE_URL, which includes DeepSeek's Anthropic-compatible
	// endpoint - see docs/12-deepseek.md for what that route does not carry).
	BackendAnthropic Backend = "anthropic"
	// BackendDeepSeek is DeepSeek's own chat completions API.
	BackendDeepSeek Backend = "deepseek"
	// BackendScripted replays a fixed script. Tests and offline demos only.
	BackendScripted Backend = "scripted"
)

// backendSpec is the per-provider table.
//
// Why a table: switching provider without changing the model ids is the obvious
// mistake, and it fails in the worst possible way - DeepSeek's compatibility
// layer silently maps an unrecognised model onto its own default, so the process
// keeps working while answering from a model nobody chose. Startup refuses that
// instead.
type backendSpec struct {
	DefaultAgent      string
	DefaultClassifier string
	// Prefix identifies model ids that belong to this provider.
	Prefix string
	// Known lists the model ids this build has been written against. An id
	// outside the list is allowed - proxies and new releases are legitimate -
	// but it is logged, not silently accepted.
	Known []string
	// RequiresKey is true where there is no other credential source.
	RequiresKey bool
	KeyEnv      string
}

var backends = map[Backend]backendSpec{
	BackendAnthropic: {
		DefaultAgent:      "claude-opus-5",
		DefaultClassifier: "claude-haiku-4-5",
		Prefix:            "claude-",
		Known:             []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-opus-4-8"},
		// The SDK also resolves an OAuth profile from `ant auth login`, so an
		// empty environment variable is not proof of no credential.
		RequiresKey: false,
		KeyEnv:      "ANTHROPIC_API_KEY",
	},
	BackendDeepSeek: {
		DefaultAgent:      "deepseek-v4-pro",
		DefaultClassifier: "deepseek-v4-flash",
		Prefix:            "deepseek-",
		Known:             []string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"},
		RequiresKey:       true,
		KeyEnv:            "DEEPSEEK_API_KEY",
	},
	BackendScripted: {
		DefaultAgent:      "scripted",
		DefaultClassifier: "scripted",
		Prefix:            "",
		RequiresKey:       false,
	},
}

func (b Backend) spec() (backendSpec, bool) {
	s, ok := backends[b]
	return s, ok
}

// BackendNames lists every supported backend, for error messages and docs.
func BackendNames() []string {
	out := make([]string, 0, len(backends))
	for b := range backends {
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// checkModelBelongs reports an error when a model id clearly belongs to a
// different provider, and a warning string when it is merely unrecognised.
func checkModelBelongs(b Backend, field, model string) (warn string, err error) {
	if model == "" {
		return "", nil
	}
	spec, ok := b.spec()
	if !ok {
		return "", nil
	}
	for other, otherSpec := range backends {
		if other == b || otherSpec.Prefix == "" {
			continue
		}
		if strings.HasPrefix(model, otherSpec.Prefix) {
			return "", fmt.Errorf("%s=%q is a model for the %s backend, but OBA_BACKEND=%s. "+
				"Either set OBA_BACKEND=%s, or set %s to one of: %s",
				field, model, other, b, other, field, strings.Join(spec.Known, ", "))
		}
	}
	if len(spec.Known) == 0 {
		return "", nil
	}
	for _, k := range spec.Known {
		if k == model {
			return "", nil
		}
	}
	return fmt.Sprintf("%s=%q is not a model id this build was written against; "+
		"proceeding anyway. Known ids for %s: %s", field, model, b, strings.Join(spec.Known, ", ")), nil
}

// SearchProvider names the web-search vendor the live lookup uses.
//
// Why an enum and not just an endpoint URL: these vendors do not share a wire
// shape. Bocha takes a POST with a bearer token and answers
// `data.webPages.value[]`; Brave takes a GET with a subscription-token header
// and answers `web.results[]`. Pointing one at the other's URL decodes cleanly
// and yields nothing, so the mistake would surface as "there are no jobs in your
// city" rather than as an error. Naming the vendor makes that unrepresentable.
type SearchProvider string

const (
	// SearchBocha (博查) is the default. It indexes the Chinese-language job and
	// public-service sites this product actually needs; see internal/livesource
	// for the measurement behind that choice.
	SearchBocha SearchProvider = "bocha"
	// SearchBrave is the Brave Search API. Kept because it works and is already
	// written, but it is not the default: its free tier was withdrawn in
	// February 2026 and its coverage of Chinese municipal sites is unproven.
	SearchBrave SearchProvider = "brave"
)

// SearchProviderNames lists the accepted values, for error messages.
func SearchProviderNames() []string {
	return []string{string(SearchBocha), string(SearchBrave)}
}
