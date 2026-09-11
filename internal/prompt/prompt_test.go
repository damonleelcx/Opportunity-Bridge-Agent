package prompt_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/guardrail"
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

// Every registered check reaches the model as a description, never as the
// fallback. verifierPlain is a hand-kept switch beside the guardrail registry,
// so a verifier registered without a case renders as a pointer: the model is
// told a check exists and not what it looks for, and pays for it in redrafts.
//
// Five were in that state, all pointing at a file that never existed. The test
// above could not see it: it asks whether the NAME appears, and the name is
// printed before the colon whether or not anything follows it.
// See docs/bugfix/2026-09-11-five-checks-were-never-described-to-the-model.md
func TestEveryRegisteredVerifierIsDescribedToTheModel(t *testing.T) {
	// The fallback is read from the code rather than restated here, so rewording
	// it cannot turn this into a comparison against nothing.
	const unknown = "a_check_nobody_registered"
	fallback := checkLine(t, prompt.IntentLayer(intent.Intent{Verifiers: []string{unknown}}), unknown)

	names := guardrail.VerifierNames()
	if len(names) == 0 {
		t.Fatal("the guardrail registry is empty; there is nothing to check against")
	}
	sort.Strings(names)
	layer := prompt.IntentLayer(intent.Intent{Verifiers: names})
	for _, name := range names {
		if got := checkLine(t, layer, name); got == fallback {
			t.Errorf("verifier %q is registered but the model is only told %q; add a case to verifierPlain", name, got)
		}
	}

	// And the fallback must not send anybody to a file that is not there.
	for _, word := range strings.Fields(fallback) {
		if path := strings.TrimRight(word, ".,;:"); strings.HasPrefix(path, "docs/") {
			if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
				t.Errorf("the fallback cites %s, which does not exist", path)
			}
		}
	}
}

// checkLine returns what the intent layer tells the model about one check.
func checkLine(t *testing.T, layer, name string) string {
	t.Helper()
	prefix := "- " + name + ": "
	for _, line := range strings.Split(layer, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("verifier %q is not listed under THIS TURN IS CHECKED FOR at all", name)
	return ""
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

// Dialect is a text capability, and it has to be reachable without a tool call.
//
// The policy used to be a refusal reachable only through the AccessDialect
// delivery rule. A live turn showed what that costs: somebody wrote in
// Cantonese, asked in Cantonese to be answered in Cantonese, the agent never
// called accessibility_set, and the answer came back in Mandarin opening with
// "我写不到标准广东话". Writing in a variety IS the request.
// See docs/bugfix/2026-08-31-dialect-moved-into-the-text.md
// flat collapses whitespace so an assertion about WORDING is not also an
// assertion about where the prompt constant happens to wrap its lines.
func flat(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func TestDialectPolicyAppliesWithoutAnyToolCall(t *testing.T) {
	// No access needs set at all — the state the live failure was in.
	ctx := flat(prompt.ContextLayer(opts("zh-CN")))
	if !strings.Contains(ctx, "answer in the variety the person is using") {
		t.Errorf("a person who simply writes in their own variety gets no dialect rule; "+
			"the capability is wired to a state nobody sets\n\n%s", ctx)
	}
}

// Three things have to survive, and the old policy had no test at all — which is
// exactly how a rule gets quietly rewritten.
func TestDialectRuleProtectsWhatThePersonMustReuse(t *testing.T) {
	for _, locale := range []string{"zh-CN", "match"} {
		low := flat(prompt.LanguageDirective(locale))
		for _, want := range []struct{ phrase, why string }{
			{"answer in the variety the person is using",
				"the capability itself: without it the rule reads as a restriction again"},
			{"official written form",
				"a programme name or phone number rendered in dialect is one the counter does not recognise"},
			{"say so",
				"the honesty fallback: where it cannot write the variety it must admit that, not imitate"},
			{"no dialect voice",
				"read-aloud speaks these characters in Mandarin, and the answer must not promise otherwise"},
		} {
			if !strings.Contains(low, want.phrase) {
				t.Errorf("[%s] the dialect policy no longer says %q — %s", locale, want.phrase, want.why)
			}
		}
	}
}

// The saved preference must add persistence, not a second copy of the policy.
// Two copies in one prompt is how two copies drift apart.
func TestSavedDialectPreferenceDoesNotRestateThePolicy(t *testing.T) {
	o := opts("zh-CN")
	o.Session.AccessNeeds = []domain.AccessNeed{domain.AccessDialect}
	ctx := prompt.ContextLayer(o)
	if n := strings.Count(flat(ctx), "answer in the variety the person is using"); n != 1 {
		t.Errorf("the dialect policy appears %d times in one prompt; it must be stated once", n)
	}
	if !strings.Contains(ctx, "standing preference") {
		t.Error("the saved preference adds nothing: a turn written in standard Chinese would lose the dialect")
	}
}

// 猎源图谱's news is news, not an escalation.
//
// Options.Alerts already existed and renders "ACT ON THIS BEFORE ANYTHING ELSE
// … call handoff_to_human". Carrying a reorganisation in that field would make
// every piece of headhunting news an escalation to a human being — the feature
// the recruiter asked for would page somebody.
func TestGraphNewsIsToldWithoutEscalating(t *testing.T) {
	ctx := prompt.ContextLayer(prompt.Options{
		Intent:    intent.MustGet(intent.TalentSourcing),
		Session:   &store.Session{Role: domain.RoleRecruiter},
		Locale:    "zh-CN",
		GraphNews: []string{"A司 — c业务组并入b业务组; 2 people you have recorded are in that group (hearsay)"},
	})
	if !strings.Contains(ctx, "c业务组并入b业务组") {
		t.Fatal("the news never reaches the model")
	}
	if strings.Contains(ctx, "ACT ON THIS BEFORE ANYTHING ELSE") {
		t.Error("the news is rendered as an input-guard escalation")
	}
	if strings.Contains(ctx, "handoff_to_human") {
		t.Error("the news tells the agent to hand the person over to a human")
	}
	// And it is only there when there is something to say.
	quiet := prompt.ContextLayer(prompt.Options{
		Intent:  intent.MustGet(intent.TalentSourcing),
		Session: &store.Session{Role: domain.RoleRecruiter},
		Locale:  "zh-CN",
	})
	if strings.Contains(quiet, "WHAT CHANGED IN THEIR GRAPH") {
		t.Error("the block is rendered with nothing in it")
	}
}
