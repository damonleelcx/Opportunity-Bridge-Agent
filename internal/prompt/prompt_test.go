package prompt_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/prompt"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func opts(locale string) prompt.Options {
	return prompt.Options{
		Intent:  intent.MustGet(intent.IndividualPathway),
		Session: &store.Session{ID: "ses_1", Role: domain.RoleResident, SubjectID: "sub_1", Locale: locale},
		Profile: domain.Profile{SubjectID: "sub_1"},
		Locale:  locale,
	}
}

// The language rule has to come before the rest of the per-turn context. A rule
// buried under a screen of profile facts and findings is the one that gets
// dropped — and everything else the model can see (this prompt, the tool
// descriptions, the corpus) is English, which pulls hard the other way.
func TestLanguageDirectiveComesFirst(t *testing.T) {
	ctx := prompt.ContextLayer(opts("zh-CN"))
	want := "ANSWER IN SIMPLIFIED CHINESE"
	di, si := strings.Index(ctx, want), strings.Index(ctx, "CURRENT SITUATION")
	if di < 0 {
		t.Fatalf("no Chinese directive in the context layer:\n%s", ctx)
	}
	if si >= 0 && di > si {
		t.Errorf("the language directive appears after the rest of the context")
	}
}

func TestLanguageDirectiveCarriesTheCarveOuts(t *testing.T) {
	// Without these two, the instruction does damage: it would push tool
	// arguments into Chinese against an English index, and "translate" an
	// address, which is the same thing as inventing one.
	for _, locale := range []string{"zh-CN", "en", "match"} {
		d := prompt.LanguageDirective(locale)
		// The corpus is Chinese, so the search language is no longer a carve-out
		// from the answer language — it is the same language. What still has to
		// be stated is that identifiers and addresses are quoted, never
		// translated.
		if !strings.Contains(d, "search with Chinese keywords") {
			t.Errorf("%s: no statement about the search language", locale)
		}
		// Matched on a fragment that survives line wrapping.
		if !strings.Contains(d, "invented address") {
			t.Errorf("%s: no carve-out for identifiers and addresses", locale)
		}
	}
}

func TestLanguageDirectivePerLocale(t *testing.T) {
	for _, tc := range []struct{ locale, want string }{
		{"zh-CN", "ANSWER IN SIMPLIFIED CHINESE"},
		{"zh", "ANSWER IN SIMPLIFIED CHINESE"},
		{"en", "ANSWER IN ENGLISH"},
		{"match", "ANSWER IN THE LANGUAGE THE PERSON WROTE IN"},
		{"", "ANSWER IN THE LANGUAGE THE PERSON WROTE IN"},
	} {
		if got := prompt.LanguageDirective(tc.locale); !strings.HasPrefix(got, tc.want) {
			t.Errorf("locale %q produced %q…", tc.locale, firstLine(got))
		}
	}
}

// The persona is a style layer and must stay subordinate to the charter, and it
// must sit in the cached layer rather than being paid for on every turn.
func TestPersonaRidesInTheCachedLayer(t *testing.T) {
	charter, intentLayer, ctx := prompt.Layers(opts("zh-CN"))
	if !strings.Contains(charter, prompt.AgentName) {
		t.Error("the agent's name is not in layer 1")
	}
	if !strings.Contains(charter, "accuracy wins") {
		t.Error("the clause subordinating warmth to accuracy is missing from layer 1")
	}
	if strings.Contains(intentLayer, "HOW YOU SPEAK") || strings.Contains(ctx, "HOW YOU SPEAK") {
		t.Error("the persona leaked out of the cached layer")
	}
}

func TestIntentLayerIsRenderedFromTheRegistry(t *testing.T) {
	// The prompt and the enforcement code must not be able to disagree, so the
	// boundaries are rendered rather than restated.
	in := intent.MustGet(intent.SupplyDemandInsight)
	layer := prompt.IntentLayer(in)
	for _, s := range in.CannotDo {
		if !strings.Contains(layer, s) {
			t.Errorf("a CannotDo boundary is missing from the prompt: %q", s)
		}
	}
	for _, v := range in.Verifiers {
		if !strings.Contains(layer, v) {
			t.Errorf("verifier %q is not disclosed to the model; an unstated test is a retry tax", v)
		}
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// The answer is rendered with textContent and spoken with the browser's
// speech synthesiser, both of which take it literally. Nothing downstream strips
// Markdown, so the only place this can be decided is here.
//
// Observed in production on 2026-08-28: an answer full of **bold** and dash
// bullets, shown to the reader with the asterisks in it. The read-aloud setting
// would have spoken them.
// See docs/bugfix/2026-08-28-answers-were-raw-markdown.md
func TestCharterForbidsMarkdown(t *testing.T) {
	for _, want := range []string{"plain text", "Markdown", "asterisk"} {
		if !strings.Contains(prompt.Charter, want) {
			t.Errorf("the charter no longer mentions %q: nothing else in the "+
				"product stops the model emitting Markdown, and the interface "+
				"shows it raw", want)
		}
	}
}
