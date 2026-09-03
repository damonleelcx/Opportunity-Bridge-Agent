package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// Backend names the model provider this process talks to.
type Backend string

const (
	// BackendQwen is Alibaba Cloud Model Studio's (DashScope) OpenAI-compatible
	// endpoint, which serves the Qwen family. This is the default and the only
	// live provider this build ships with.
	BackendQwen Backend = "qwen"
	// BackendScripted replays a fixed script. Tests and offline demos only - it
	// is a fixture, not a provider, which is why it needs no key and prices
	// nothing.
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
	BackendQwen: {
		DefaultAgent:      llm.QwenAgentModel,
		DefaultClassifier: llm.QwenClassifierModel,
		Prefix:            "qwen",
		Known:             llm.QwenKnownModels,
		// There is no OAuth or ambient-credential path here: an empty
		// QWEN_API_KEY means there is no credential, so startup can say so
		// rather than letting the first person's question discover it.
		RequiresKey: true,
		KeyEnv:      "QWEN_API_KEY",
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

// retiredBackendModels are model ids that used to be valid here, from backends
// this build no longer ships.
//
// # Why this table exists, and why it is an ERROR rather than a warning
//
// Until 2026-09-03 this application shipped Anthropic and DeepSeek backends. A
// deployment upgrading in place keeps its .env, and that .env very likely still
// says OBA_AGENT_MODEL=claude-opus-5 or deepseek-v4-pro. Those ids are now
// pointed at Model Studio, and the two of them fail in OPPOSITE ways:
//
//   - claude-opus-5 returns a clean 404. Loud, and easy to diagnose.
//   - deepseek-v4-pro returns HTTP 200 AND A REAL ANSWER - verified against the
//     live service on 2026-09-03. Model Studio is a multi-vendor marketplace and
//     genuinely hosts DeepSeek, so the call succeeds, bills at a rate this build
//     has no price for, and answers from a model nobody selected in the new
//     configuration.
//
// The second is the failure this whole table was written to prevent: the process
// keeps working while quietly doing something other than what was configured.
// Refused at startup, because a person who upgraded and did nothing else has not
// chosen that - they simply have not read the release note yet.
//
// 🚫 This is not a general "foreign model" check. Model Studio legitimately
// serves ZHIPU/, stepfun/ and deepseek- ids, and someone may deliberately want
// one. Only ids that were OUR OWN defaults are refused, because only those can
// be present by pure inertia. Delete this table once no deployment carries a
// pre-2026-09 .env.
var retiredBackendModels = map[string]string{
	"claude-opus-5":                "anthropic",
	"claude-sonnet-5":              "anthropic",
	"claude-haiku-4-5":             "anthropic",
	"claude-opus-4-8":              "anthropic",
	"deepseek-v4-pro":              "deepseek",
	"deepseek-v4-flash":            "deepseek",
	"deepseek-v4-flash-vision-exp": "deepseek",
}

// checkModelBelongs reports an error when a model id is a leftover from a
// backend this build has removed, and a warning string when it is merely
// unrecognised.
func checkModelBelongs(b Backend, field, model string) (warn string, err error) {
	if model == "" {
		return "", nil
	}
	spec, ok := b.spec()
	if !ok {
		return "", nil
	}
	if from, retired := retiredBackendModels[model]; retired {
		return "", fmt.Errorf("%s=%q is a model from the %s backend, which this build no longer "+
			"supports. It was not simply removed: %s still answers on OBA_BACKEND=%s, billed at a "+
			"rate this build has no price for, so leaving it set would keep working while answering "+
			"from a model you did not choose. Set %s to one of: %s",
			field, model, from, model, b, field, strings.Join(spec.Known, ", "))
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
