package web_test

// Fences over the embedded interface.
//
// There is no JavaScript test runner in this repository, and adding one to
// assert two properties would be a build system nobody asked for. These read
// the shipped assets out of the embed and assert on their source. That is a
// weaker instrument than executing the code — it can only catch a rule being
// removed, not a rule being wrong — so it is used only where the failure is
// silent and the cost is somebody's wasted journey.

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := web.Files.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The opportunity panel must consult the live results before it says nothing
// was found.
//
// It used to test only `results`, the corpus records, which are empty for every
// city the corpus does not cover — which is every city but one. So the panel
// said "nothing matched this time" directly underneath an answer listing five
// real openings the live lookup had just returned. The panel is the part people
// scan, and it was contradicting the agent.
// See docs/bugfix/2026-08-28-live-results-shown-as-nothing.md
func TestOpportunityPanelConsultsLiveResultsBeforeSayingNothingMatched(t *testing.T) {
	src := asset(t, "app.js")

	start := strings.Index(src, "function opportunityList(")
	if start < 0 {
		t.Fatal("opportunityList is gone; this fence no longer guards anything")
	}
	end := strings.Index(src[start:], "\nfunction ")
	if end < 0 {
		t.Fatal("could not find the end of opportunityList")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "live_results") {
		t.Error("opportunityList does not read live_results: for any city outside the corpus " +
			"the panel will claim nothing was found while the answer lists real openings")
	}
	// The empty state must depend on BOTH collections. Guarding only on
	// `!r.results?.length` is the exact defect this replaced.
	guard := regexp.MustCompile(`if \(!r\.results\?\.length && !live\.length\)`)
	if !guard.MatchString(body) {
		t.Error(`the "nothing matched" branch is not guarded on both r.results and the live results; ` +
			`it must only be reached when there is genuinely nothing to show`)
	}
}

// A reset takes the failed attempt off the screen.
//
// Deltas now stream through as the model writes them, so a failed attempt has
// already put text in front of the reader by the time it fails. The reset event
// is what makes that safe; a client that ignores it shows half an answer
// followed by a different whole one — which is the exact thing the old
// buffer-everything approach was protecting against, and the reason it cost the
// product its streaming. See docs/bugfix/2026-08-28-answers-never-streamed.md
func TestClientClearsTheScreenOnReset(t *testing.T) {
	src := asset(t, "app.js")

	start := strings.Index(src, `case "text":`)
	if start < 0 {
		t.Fatal("the text branch is gone; this fence no longer guards anything")
	}
	end := strings.Index(src[start:], "case \"tool_start\":")
	if end < 0 {
		t.Fatal("could not find the end of the text branch")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "ev.reset") {
		t.Error("the text branch never reads ev.reset: a retried turn leaves the " +
			"failed attempt's text on screen with the new answer appended to it")
	}
	if !strings.Contains(body, `streamed = ""`) {
		t.Error("reset does not clear the accumulated text")
	}
	if !strings.Contains(body, "awaitingText") {
		t.Error("reset leaves an empty bubble rather than the typing indicator; " +
			"an empty grey box reads as a broken answer")
	}
}

// A live lead says whether it is a job or a course.
//
// The lookup already knows — it asked the web a different question for each —
// and the two carry different warnings. Rendering both under one badge puts the
// reader back to guessing from a page title, which is what the intent field
// exists to stop. See docs/bugfix/2026-08-28-live-search-never-looked-for-training.md
func TestLiveCardTellsACourseFromAnOpening(t *testing.T) {
	src := asset(t, "app.js")

	start := strings.Index(src, "function liveCard(")
	if start < 0 {
		t.Fatal("liveCard is gone; this fence no longer guards anything")
	}
	end := strings.Index(src[start:], "\nfunction ")
	if end < 0 {
		t.Fatal("could not find the end of liveCard")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "x.intent") {
		t.Error("liveCard never reads x.intent: a course and an opening render " +
			"under the same badge, and the reader is back to guessing from the title")
	}
	if !strings.Contains(body, "card.liveCourse") {
		t.Error("liveCard has no course badge to render")
	}
}

// Every string the interface can render must exist in every language.
//
// A missing key does not fail loudly — it renders as the key itself or as
// nothing at all, in the one language nobody testing in Chinese would look at.
// This is the check that a string added to one table was added to both.
func TestEveryInterfaceStringExistsInEveryLanguage(t *testing.T) {
	src := asset(t, "i18n.js")

	// STRINGS is the table; it ends at the first line that is exactly "};".
	tableStart := strings.Index(src, "const STRINGS = {")
	if tableStart < 0 {
		t.Fatal("the STRINGS table is gone; this fence no longer guards anything")
	}
	table := src[tableStart:]
	if end := strings.Index(table, "\n};"); end > 0 {
		table = table[:end]
	}

	zhStart := strings.Index(table, `"zh-CN": {`)
	enStart := strings.Index(table, "\n  en: {")
	if zhStart < 0 || enStart < 0 || enStart < zhStart {
		t.Fatalf("could not locate both language blocks (zh at %d, en at %d)", zhStart, enStart)
	}
	key := regexp.MustCompile(`"([a-zA-Z][\w.]*)":`)
	keysIn := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, m := range key.FindAllStringSubmatch(s, -1) {
			out[m[1]] = true
		}
		return out
	}
	zh := keysIn(table[zhStart:enStart])
	en := keysIn(table[enStart:])
	if len(zh) < 50 || len(en) < 50 {
		t.Fatalf("parsed too few keys (zh=%d en=%d); the fence is not reading the table",
			len(zh), len(en))
	}

	for k := range zh {
		if k == "zh-CN" {
			continue
		}
		if !en[k] {
			t.Errorf("%q exists in zh-CN but not in en: it renders as nothing for an English reader", k)
		}
	}
	for k := range en {
		if !zh[k] {
			t.Errorf("%q exists in en but not in zh-CN: it renders as nothing for the default audience", k)
		}
	}
}

// Read-aloud must name the voice it wants, and must still speak when it cannot
// have it.
//
// Two failures sit on top of each other here. The first is that
// SpeechSynthesisVoice carries no gender, so a chosen voice can only be a named
// one — the moment this goes back to setting `lang` alone, the reader is handed
// whatever the platform happens to default to, which on some machines is a male
// voice and on others an elderly one. The second is worse and quieter: if a
// missing voice were treated as a reason not to speak, read-aloud would switch
// itself off on exactly the machines whose voice list is thin, for the people
// who cannot read the answer off the screen.
func TestReadAloudPicksANamedVoiceAndStillSpeaksWithoutOne(t *testing.T) {
	src := asset(t, "app.js")

	if !strings.Contains(src, "const READING_VOICES = {") {
		t.Fatal("READING_VOICES is gone; read-aloud is back to the platform default voice")
	}
	for _, lang := range []string{`"zh-CN": [`, `"en-US": [`} {
		if !strings.Contains(src, lang) {
			t.Errorf("READING_VOICES has no %s block: that language falls back to the platform default", lang)
		}
	}

	// The local-voice logic lives in speakLocally, not speak: speak now chooses
	// between the vendor and this, and speakLocally is the branch that has to
	// pick a named voice. The invariants below are unchanged by that move.
	body := section(t, src, "function speakLocally(")

	if !strings.Contains(body, "readingVoice(") || !strings.Contains(body, "u.voice = voice") {
		t.Error("speakLocally does not set a chosen voice: the answer is read by whatever the platform defaults to")
	}
	// The voice must be optional. `if (voice)` guarding the assignment, and a
	// lang set on both branches, is what keeps a thin voice list from silencing
	// the feature.
	if !strings.Contains(body, "if (voice) u.voice = voice;") {
		t.Error("the chosen voice is not optional: a machine without it may end up speaking nothing")
	}
	if !regexp.MustCompile(`u\.lang = voice \? voice\.lang : lang;`).MatchString(body) {
		t.Error("u.lang is not set on both branches: with no voice found nothing selects the language")
	}

	// Exact language matching, not prefix. Half this product's Chinese voices on
	// a Mac are zh-TW, and a mainland answer must not be read in a Taiwanese
	// accent because "zh" matched.
	if !strings.Contains(src, "function sameLanguage(") {
		t.Fatal("sameLanguage is gone; voice selection can match zh-TW for a zh-CN reader")
	}
	pick := src[strings.Index(src, "function readingVoice("):]
	if end := strings.Index(pick, "\nfunction "); end > 0 {
		pick = pick[:end]
	}
	if !strings.Contains(pick, "sameLanguage(v.lang, lang)") {
		t.Error("readingVoice does not compare languages exactly; a zh-TW voice can be picked for zh-CN")
	}
	if !strings.Contains(pick, "if (!available.length) return null;") {
		t.Error("readingVoice caches a lookup made against an empty voice list: " +
			"Chrome loads voices asynchronously, so the miss would be cached forever")
	}
}

// A speech vendor that is off, unreachable or refusing must still leave the
// answer readable aloud.
//
// This is the fence that matters most in the read-aloud path, and it guards a
// failure that is invisible from the outside: with a vendor wired in, every way
// the vendor can fail — no key on this deployment, a network error, a 402 when
// the credit runs out, a browser autoplay policy refusing to play the audio —
// ends in the person pressing 读给我听 and hearing nothing at all. Nothing on
// screen changes, no error is shown, and the feature simply looks unused. The
// built-in voice is the recovery, and every one of those paths has to reach it.
func TestVendorSpeechAlwaysFallsBackToTheBrowsersOwnVoice(t *testing.T) {
	src := asset(t, "app.js")

	for _, fn := range []string{"function speak(", "function speakWithVendor(", "function speakLocally("} {
		if !strings.Contains(src, fn) {
			t.Fatalf("%s is gone; this fence no longer guards anything", fn)
		}
	}

	speak := section(t, src, "async function speak(")
	if !strings.Contains(speak, "speakLocally(") {
		t.Error("speak never reaches speakLocally: when the vendor is off or failing, " +
			"pressing read-aloud produces silence and says nothing")
	}
	// The fallback must be unconditional-on-failure: `await speakWithVendor(...)`
	// returning false has to fall through to the local voice on the same path.
	if !regexp.MustCompile(`if \(vendorVoice !== false && await speakWithVendor\(body\)\) return;\s*\n\s*speakLocally\(body\);`).MatchString(speak) {
		t.Error("the local voice is not the unconditional next step after a failed vendor render")
	}

	vendor := section(t, src, "async function speakWithVendor(")
	// Every exit that is not "audio is playing" must be false, including the
	// ones people forget: a thrown fetch, a non-ok status, an empty blob, and a
	// rejected play() — autoplay policy is a refusal, not an error.
	for _, need := range []string{"catch", "if (!r.ok) return false;", "if (!blob.size) return false;"} {
		if !strings.Contains(vendor, need) {
			t.Errorf("speakWithVendor has no %q path: that failure reaches the person as silence", need)
		}
	}
	if !strings.Contains(vendor, "vendorVoice = false;") {
		t.Error("a 503 does not disable the vendor for the session: an unkeyed deployment " +
			"makes one failed request per answer forever")
	}

	// Switching read-aloud off has to silence BOTH paths. Cancelling only
	// speechSynthesis leaves vendor audio playing after the person opted out.
	if !strings.Contains(src, "function stopSpeaking(") {
		t.Fatal("stopSpeaking is gone; the read-aloud toggle can no longer stop vendor audio")
	}
	stop := section(t, src, "function stopSpeaking(")
	if !strings.Contains(stop, "speechSynthesis?.cancel()") || !strings.Contains(stop, "vendorAudio.pause()") {
		t.Error("stopSpeaking does not stop both the browser voice and the vendor audio")
	}
	if strings.Contains(src, "if (!e.target.checked) window.speechSynthesis?.cancel();") {
		t.Error("the read-aloud toggle cancels only the browser voice; vendor audio keeps playing " +
			"after the person switches it off")
	}
}

// section returns one function body, from its declaration to the next
// top-level function.
func section(t *testing.T, src, decl string) string {
	t.Helper()
	start := strings.Index(src, decl)
	if start < 0 {
		t.Fatalf("%q not found", decl)
	}
	rest := src[start+len(decl):]
	end := strings.Index(rest, "\nfunction ")
	if a := strings.Index(rest, "\nasync function "); a >= 0 && (end < 0 || a < end) {
		end = a
	}
	if end < 0 {
		return src[start:]
	}
	return src[start : start+len(decl)+end]
}

// ── the landing page at `/` ────────────────────────────────────────────────
//
// Three fences, and they share a shape: each guards a failure that is invisible
// to whoever introduces it and visible only to a reader they will never meet.

// Every string the landing page asks for exists in both languages.
//
// A missing key does not throw. `t()` falls back to the key itself, so the front
// page renders "home.hero.title" as its headline — in one language only, which
// is exactly the language the author was not reading. This is the first page a
// stranger sees, and half of them read the other column of that table.
//
// The check is structural rather than semantic: it splits STRINGS into its two
// locale blocks and asserts the key appears in each. It cannot tell whether the
// translation is any good, only that one is there — which is the failure worth
// catching automatically.
func TestLandingPageStringsExistInBothLanguages(t *testing.T) {
	html := asset(t, "index.html")
	i18n := asset(t, "i18n.js")

	zh, en := localeBlocks(t, i18n)

	keys := regexp.MustCompile(`data-i18n(?:-aria|-title|-ph)?="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(keys) == 0 {
		t.Fatal("the landing page carries no data-i18n keys; this fence no longer guards anything")
	}
	seen := map[string]bool{}
	for _, m := range keys {
		key := m[1]
		if seen[key] {
			continue
		}
		seen[key] = true
		quoted := `"` + key + `":`
		if !strings.Contains(zh, quoted) {
			t.Errorf("%s has no zh-CN string: the landing page will render the key itself", key)
		}
		if !strings.Contains(en, quoted) {
			t.Errorf("%s has no English string: the landing page will render the key itself", key)
		}
	}
}

// The Chinese written into the landing page's markup is the Chinese in the table.
//
// The page ships its default language as real text in the HTML, so it reads
// correctly before any script runs. That leaves two copies of every Chinese
// string, and editing only the markup produces a page that looks right until
// somebody touches the language control — at which point setLocale sweeps
// textContent and the older wording from the table snaps back over the newer
// wording on screen. Nothing throws; it just quietly un-edits itself.
//
// Escaped or multi-line values are skipped rather than guessed at: this compares
// literal source text, and a false red here would be worse than a small gap.
func TestLandingPageMarkupAgreesWithTheChineseTable(t *testing.T) {
	html := asset(t, "index.html")
	zh, _ := localeBlocks(t, asset(t, "i18n.js"))

	node := regexp.MustCompile(`data-i18n="(home\.[^"]+)"[^>]*>([^<]*)<`)
	matches := node.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		t.Fatal("no landing-page text nodes found; this fence no longer guards anything")
	}
	checked := 0
	for _, m := range matches {
		key, text := m[1], m[2]
		if strings.ContainsAny(text, "\"\\\n") || text == "" {
			continue
		}
		checked++
		if !strings.Contains(zh, `"`+key+`": "`+text+`",`) {
			t.Errorf("%s reads %q in the markup but the zh-CN table says something else; "+
				"the first language switch will replace what the page shows", key, text)
		}
	}
	if checked == 0 {
		t.Fatal("every text node was skipped; this fence checked nothing")
	}
	t.Logf("%d landing-page text nodes matched the zh-CN table", checked)
}

// The landing page loads nothing over the network.
//
// The whole deployability claim — that this binary runs at a service window on a
// closed network — rests on it. A CDN stylesheet, a Google font or a remote image
// works perfectly on the machine of whoever added it and leaves the people this
// is for looking at unstyled text, with no error to tell them why. The mockup
// this was built from does load all three; see docs/14-interface.md.
//
// Links a person clicks are fine and are not what this checks: it looks only at
// subresources the document fetches on its own.
func TestLandingPageFetchesNothingFromTheNetwork(t *testing.T) {
	html := asset(t, "index.html")

	for _, pat := range []struct{ re, what string }{
		{`<link[^>]+href="(?:https?:)?//`, "a stylesheet or icon from another origin"},
		{`\ssrc="(?:https?:)?//`, "a script or image from another origin"},
		{`@import\s+url\(\s*["']?(?:https?:)?//`, "an imported stylesheet from another origin"},
	} {
		if regexp.MustCompile(pat.re).MatchString(html) {
			t.Errorf("the landing page loads %s: it will render unstyled on a closed network", pat.what)
		}
	}
	for _, css := range []string{"home.css", "tokens.css", "avatar.css"} {
		if strings.Contains(asset(t, css), "://") {
			t.Errorf("%s references another origin", css)
		}
	}
}

// Nothing on the landing page is hidden unless the script is known to be running.
//
// The reveal-on-scroll animation hides sections and an observer puts them back.
// If the hiding rule is ever written without the `html.js` guard, then a reader
// whose script was blocked, delayed or broken gets a column of empty space —
// and the author, whose script ran, sees nothing wrong. `js` is stamped on
// <html> by an inline script in the head, so it is only ever present when the
// script actually arrived.
func TestLandingPageHidesNothingWithoutJavaScript(t *testing.T) {
	css := asset(t, "home.css")

	found := false
	for _, line := range strings.Split(css, "\n") {
		if !strings.Contains(line, ".reveal") || !strings.Contains(line, "opacity: 0") {
			continue
		}
		found = true
		if !strings.HasPrefix(strings.TrimSpace(line), "html.js ") {
			t.Errorf("`%s` hides content without the html.js guard: a reader whose script "+
				"never arrives gets a blank page", strings.TrimSpace(line))
		}
	}
	if !found {
		t.Skip("nothing on the landing page is hidden for the animation; this fence has nothing to guard")
	}
	if !strings.Contains(asset(t, "index.html"), `classList.add("js")`) {
		t.Error(`nothing stamps "js" on <html>, so html.js .reveal never matches and the ` +
			`animation can never reveal anything`)
	}

	// ...and it must survive a document that is never rendered.
	//
	// IntersectionObserver callbacks are delivered from "update the rendering",
	// which a hidden document does not run — measured on the live site: zero
	// callbacks in 800ms at visibilityState "hidden", with rAF silent beside it.
	// A prerender, a background tab, a print job or a crawler taking a link
	// preview would otherwise snapshot every section at opacity 0. setTimeout is
	// the only timer that still runs there, so the recovery has to hang off it.
	js := asset(t, "home.js")
	if !strings.Contains(js, "document.hidden") || !strings.Contains(js, "setTimeout") {
		t.Error("home.js has no recovery for a document that is never rendered: a crawler, " +
			"a prerender or a background tab will capture the landing page completely blank")
	}
}

// localeBlocks splits the STRINGS table into its zh-CN half and its English
// half. Structural, on purpose: the alternative is executing the module, and
// there is no JavaScript runtime in this test suite.
func localeBlocks(t *testing.T, i18n string) (zh, en string) {
	t.Helper()
	zhStart := strings.Index(i18n, `"zh-CN": {`)
	enStart := strings.Index(i18n, "\n  en: {")
	if zhStart < 0 || enStart < 0 || enStart < zhStart {
		t.Fatal("the STRINGS table no longer has a zh-CN block followed by an en block; " +
			"this fence cannot tell the two languages apart any more")
	}
	enEnd := strings.Index(i18n[enStart:], "\n};")
	if enEnd < 0 {
		t.Fatal("could not find the end of the STRINGS table")
	}
	return i18n[zhStart:enStart], i18n[enStart : enStart+enEnd]
}

// A gradient headline stays readable where the gradient does not work.
//
// The standard recipe for gradient text is `background-clip: text` plus
// `color: transparent`, and it has one catastrophic failure mode: where the clip
// is unsupported the colour still applies, so the headline is not un-gradiented
// — it is INVISIBLE. The page's largest words disappear and nothing errors.
//
// So the solid colour has to be the rule's real value and the clip may only
// replace it inside @supports. This checks the ordering rather than the styling:
// every `color: transparent` in the landing page's stylesheet must sit after the
// @supports guard that earns it.
func TestLandingPageGradientTextHasASolidFallback(t *testing.T) {
	// Comments stripped first. These fences read source text, and this file's
	// own commentary explains the very patterns being searched for — the first
	// version reported a defect that was a sentence in a comment about it.
	css := stripCSSComments(asset(t, "home.css"))

	guard := strings.Index(css, "@supports ((background-clip: text)")
	// Not strings.Index("color: transparent"): `border-color: transparent` is a
	// perfectly ordinary declaration and contains that substring. The first
	// version of this fence matched one and reported a defect that was not there.
	transparent := -1
	if loc := regexp.MustCompile(`(^|[^-\w])color:\s*transparent`).FindStringIndex(css); loc != nil {
		transparent = loc[0]
	}

	if transparent < 0 {
		t.Skip("nothing on the landing page uses transparent text; this fence has nothing to guard")
	}
	if guard < 0 {
		t.Fatal("home.css sets `color: transparent` with no @supports guard for background-clip: " +
			"where the clip is unsupported the headline is invisible, not merely un-gradiented")
	}
	if transparent < guard {
		t.Error("`color: transparent` appears before the @supports guard, so it applies " +
			"unconditionally: the headline disappears wherever background-clip: text is missing")
	}
	// ...and the guarded rule must have had a real colour to fall back to.
	rule := css[strings.Index(css, ".grad {"):]
	rule = rule[:strings.Index(rule, "}")]
	if !strings.Contains(rule, "color:") || strings.Contains(rule, "transparent") {
		t.Error(".grad states no solid colour outside the @supports block; there is nothing " +
			"for the headline to fall back to")
	}
}

// --brand is the accent. --brand-fill is what goes behind --brand-ink.
//
// This is the system's one load-bearing invariant and it is worth a fence
// because the wrong token has the more obvious name. --brand has to be LIGHT on
// a near-black canvas so it reads as text and as icons; putting white on it
// gives 2.6:1, which is unreadable and looks merely "soft" to anyone with good
// eyes and a good screen. --brand-fill is deep enough to carry white, measured.
//
// The failure is silent in the worst way: nothing throws, the button renders,
// and it is the people reading in daylight on a cheap panel who cannot use it.
func TestFilledSurfacesUseTheFillTokenNotTheAccent(t *testing.T) {
	filled := regexp.MustCompile(`background(?:-color)?:\s*var\(--brand\)`)

	for _, name := range []string{"styles.css", "home.css", "avatar.css"} {
		css := stripCSSComments(asset(t, name))
		for _, m := range filled.FindAllString(css, -1) {
			t.Errorf("%s: %q — a filled surface must use var(--brand-fill) or var(--aurora-fill). "+
				"--brand is the accent colour and is light on dark; white on it is 2.6:1", name, m)
		}
	}
}

// stripCSSComments removes /* … */ so a fence reads declarations rather than the
// prose explaining them.
func stripCSSComments(css string) string {
	var b strings.Builder
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			b.WriteString(css)
			return b.String()
		}
		b.WriteString(css[:i])
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			return b.String()
		}
		css = css[i+j+2:]
	}
}

// Every token the stylesheets reach for is defined, in every theme.
//
// The failure this guards is what a palette rewrite does: a token gets renamed
// or dropped, one stylesheet still references it, and `var(--gone)` resolves to
// nothing. No error, no warning — a border vanishes, or a colour falls back to
// inherited black on black. Measured live after the redesign: 48 distinct tokens
// referenced across the four stylesheets.
//
// The second half is the one that actually bites: a token defined for dark and
// forgotten for light. tokens.css says in its own header that defining a colour
// in only one theme is how a toggle ends up half-working; this is that comment
// with teeth.
func TestEveryTokenIsDefinedInEveryTheme(t *testing.T) {
	ref := regexp.MustCompile(`var\(\s*(--[\w-]+)`)
	// Not line-anchored: tokens.css groups related tokens on one line
	// (`--ok: …; --ok-soft: …; --ok-border: …;`), and anchoring found only the
	// first of each group — this fence's first run reported 14 tokens as
	// undefined that were defined three characters later on the same line.
	def := regexp.MustCompile(`(--[\w-]+)\s*:`)

	referenced := map[string]bool{}
	for _, name := range []string{"styles.css", "home.css", "avatar.css", "tokens.css"} {
		for _, m := range ref.FindAllStringSubmatch(stripCSSComments(asset(t, name)), -1) {
			referenced[m[1]] = true
		}
	}
	if len(referenced) < 20 {
		t.Fatalf("only %d tokens referenced; this fence is not reading the stylesheets", len(referenced))
	}

	tokens := stripCSSComments(asset(t, "tokens.css"))
	defined := map[string]bool{}
	for _, m := range def.FindAllStringSubmatch(tokens, -1) {
		defined[m[1]] = true
	}
	for name := range referenced {
		if !defined[name] {
			t.Errorf("%s is used by a stylesheet but defined nowhere in tokens.css: "+
				"it resolves to nothing, silently", name)
		}
	}

	// Light lives in the bare `:root` block; dark in `:root[data-theme="dark"]`.
	// Whatever one of them defines, the other has to define too.
	light := blockAfter(t, tokens, ":root, :root[data-theme=\"light\"] {", "--bg:")
	dark := blockAfter(t, tokens, ":root[data-theme=\"dark\"] {", "--bg:")
	if n := len(def.FindAllString(light, -1)); n < 15 {
		t.Fatalf("the light palette block has only %d tokens in it; blockAfter has "+
			"latched onto the wrong rule and the symmetry check below is vacuous", n)
	}
	for _, pair := range []struct{ a, b, an, bn string }{
		{light, dark, "light", "dark"},
		{dark, light, "dark", "light"},
	} {
		for _, m := range def.FindAllStringSubmatch(pair.a, -1) {
			if !strings.Contains(pair.b, m[1]+":") {
				t.Errorf("%s is defined for %s but not for %s: the theme toggle half-works, "+
					"and the half that breaks is whichever one nobody develops in", m[1], pair.an, pair.bn)
			}
		}
	}
}

// blockAfter returns the body of the rule matching `sel` that contains `marker`.
//
// The marker is not optional politeness. tokens.css opens with one-line
// `color-scheme` rules using the SAME selectors as the palette blocks, so a
// plain first-match returns `color-scheme: light;` — and the theme-symmetry
// comparison above then runs over a block with one declaration in it and passes
// no matter what. It did exactly that until a mutation drill found it: removing
// a token from the light palette did not turn this red, because the fence was
// never looking at the light palette.
func blockAfter(t *testing.T, css, sel, marker string) string {
	t.Helper()
	for from := 0; ; {
		i := strings.Index(css[from:], sel)
		if i < 0 {
			t.Fatalf("tokens.css has no %q block containing %q; this fence cannot compare themes",
				sel, marker)
		}
		i += from
		rest := css[i+len(sel):]
		j := strings.Index(rest, "}")
		if j < 0 {
			t.Fatalf("could not find the end of %q", sel)
		}
		if body := rest[:j]; strings.Contains(body, marker) {
			return body
		}
		from = i + len(sel)
	}
}

// The palette stays readable, not just documented.
//
// tokens.css carries measured contrast ratios in its comments. Comments do not
// re-measure themselves when somebody nudges a hex value two shades brighter to
// make a mockup look better, and the result — small grey metadata at 3.8:1 — is
// invisible to whoever made the change on a good screen in a dark room, and
// unusable at a service window in daylight. These are the pairs that carry text.
func TestPaletteMeetsContrast(t *testing.T) {
	tokens := stripCSSComments(asset(t, "tokens.css"))
	light := blockAfter(t, tokens, ":root, :root[data-theme=\"light\"] {", "--bg:")
	dark := blockAfter(t, tokens, ":root[data-theme=\"dark\"] {", "--bg:")

	// text token, background token, what it is
	pairs := []struct{ fg, bg, what string }{
		{"--ink-900", "--bg", "headline on the canvas"},
		{"--ink-700", "--bg", "body on the canvas"},
		{"--ink-500", "--bg", "muted on the canvas"},
		{"--ink-400", "--bg", "faint metadata on the canvas"},
		{"--ink-400", "--surface", "faint metadata on a card"},
		{"--ink-500", "--surface-2", "muted on the inset surface"},
		{"--brand", "--surface", "accent on a card"},
		{"--ok", "--bg", "the met state"},
		{"--warn", "--bg", "the unsure state"},
		{"--stop", "--bg", "the blocked state"},
	}
	for _, theme := range []struct {
		name, block string
	}{{"light", light}, {"dark", dark}} {
		for _, p := range pairs {
			fg, okf := hexToken(theme.block, p.fg)
			bg, okb := hexToken(theme.block, p.bg)
			if !okf || !okb {
				continue // not a plain hex (rgba tints); nothing to compute
			}
			if r := contrast(fg, bg); r < 4.5 {
				t.Errorf("%s: %s on %s is %.2f:1, below 4.5 — %s",
					theme.name, p.fg, p.bg, r, p.what)
			}
		}
	}
	// --aur-* are decorative: the bubble's rim, hairlines, the wash behind the
	// hero. They are NOT checked as text above, and that exemption is only
	// honest while nothing renders text in them — one of them is champagne, and
	// champagne on a warm white page is 2.95:1. So the exemption is enforced
	// rather than assumed.
	for _, name := range []string{"styles.css", "home.css", "avatar.css"} {
		css := stripCSSComments(asset(t, name))
		if m := regexp.MustCompile(`(^|[^-\w])color:\s*var\(--aur-\d`).FindString(css); m != "" {
			t.Errorf("%s uses an --aur-* stop as a text colour. Those are decorative and are "+
				"exempt from the contrast pairs above; if one is to carry text, add it to "+
				"that list and pick a value that clears 4.5:1", name)
		}
	}

	// The one filled treatment, per theme. --brand-fill carries --brand-ink and
	// nothing else does: it is the primary button, the person's own message and
	// the send button. It inverts with the theme, so checking one would prove
	// nothing about the other — which is where it would actually break.
	for _, theme := range []struct{ name, block string }{{"light", light}, {"dark", dark}} {
		ink, oki := hexToken(theme.block, "--brand-ink")
		fill, okf := hexToken(theme.block, "--brand-fill")
		if !oki || !okf {
			t.Errorf("%s does not define --brand-fill and --brand-ink as plain hex; the one "+
				"filled treatment in the product is unchecked in this theme", theme.name)
			continue
		}
		if r := contrast(ink, fill); r < 4.5 {
			t.Errorf("%s: --brand-ink on --brand-fill is %.2f:1, below 4.5 — the primary "+
				"button and the person's own message become unreadable", theme.name, r)
		}
	}
}

var hexRe = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)

// hexToken reads a token out of one theme block, reporting whether it is a plain
// six-digit hex. rgba() tints are used for soft fills that never carry text.
func hexToken(block, name string) (string, bool) {
	re := regexp.MustCompile(name + `\s*:\s*([^;]+);`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		return "", false
	}
	v := strings.TrimSpace(m[1])
	if !hexRe.MatchString(v) {
		return "", false
	}
	return v, true
}

func contrast(a, b string) float64 {
	la, lb := relLum(a), relLum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relLum(hex string) float64 {
	var c [3]float64
	for i := 0; i < 3; i++ {
		var n int
		fmt.Sscanf(hex[1+i*2:3+i*2], "%02x", &n)
		v := float64(n) / 255
		if v <= 0.03928 {
			c[i] = v / 12.92
		} else {
			c[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

// Every function the landing page calls actually exists.
//
// This is the second boot-time ReferenceError in home.js in one sitting. Both
// killed the whole module: the first was a `const` read from above its own
// declaration, this one was a helper deleted along with the block above it,
// because the deletion was anchored on two comments and the helper sat between
// them. Both times the page rendered and then simply did nothing — no theme
// button, no language switch — and both times `node --check` passed, because a
// syntax check does not resolve names.
//
// It is a heuristic, deliberately a narrow one: it looks at calls to bare
// lowercase identifiers, skips anything reached through a dot, and carries an
// explicit list of the globals this file legitimately uses. That is enough to
// catch a helper that no longer exists, which is the failure that keeps
// happening, without pretending to be a JavaScript engine.
func TestLandingScriptCallsNothingItDoesNotHave(t *testing.T) {
	src := stripJSComments(asset(t, "home.js"))

	have := map[string]bool{}
	for _, m := range regexp.MustCompile(`function\s+([A-Za-z_$][\w$]*)\s*\(`).FindAllStringSubmatch(src, -1) {
		have[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=`).FindAllStringSubmatch(src, -1) {
		have[m[1]] = true
	}
	// import { a, b as c } from "…"
	for _, m := range regexp.MustCompile(`import\s*\{([^}]*)\}`).FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(m[1], ",") {
			part = strings.TrimSpace(part)
			if i := strings.LastIndex(part, " "); i >= 0 {
				part = part[i+1:]
			}
			if part != "" {
				have[part] = true
			}
		}
	}
	for _, g := range []string{
		"fetch", "setTimeout", "clearTimeout", "setInterval", "matchMedia",
		"requestAnimationFrame", "parseInt", "parseFloat", "isNaN",
		"encodeURIComponent", "decodeURIComponent", "alert", "confirm",
		"if", "for", "while", "switch", "catch", "return", "typeof", "function",
		"new", "await", "else", "do", "in", "of", "delete", "void", "yield",
	} {
		have[g] = true
	}

	// A bare call: an identifier followed by "(", not preceded by "." or a word
	// character (so `x.foo(` and `notfoo(` are both skipped).
	call := regexp.MustCompile(`(^|[^\w$.])([a-z_$][\w$]*)\s*\(`)
	missing := map[string]bool{}
	for _, m := range call.FindAllStringSubmatch(src, -1) {
		if !have[m[2]] {
			missing[m[2]] = true
		}
	}
	for name := range missing {
		t.Errorf("home.js calls %s() but neither defines nor imports it: the module throws "+
			"on load and every control on the landing page stops working", name)
	}
}

// stripJSComments removes // and /* */ so a fence reads code rather than prose.
// String literals are left alone; nothing here needs to tell them apart, and a
// pretend tokeniser would be more wrong than this is.
func stripJSComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	return regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")
}

// The corpus tally has one producer, and it is not the copy.
//
// The "honest limits" section said "21 条岗位、12 份办事指南" while the corpus
// held 26 records — the national layer added five and the sentence did not
// move. A number typed into prose has no way to know that. It now comes from
// /api/meta, and this holds the arrangement in place: the claim carries no
// number at all, the tally is a separate string with both placeholders in both
// languages, and the script actually substitutes them.
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
func TestCorpusTallyIsNotWrittenIntoTheCopy(t *testing.T) {
	zh, en := localeBlocks(t, asset(t, "i18n.js"))

	// The claim itself must be number-free: any digit in it is a fact about the
	// corpus that nothing will ever update.
	claim := regexp.MustCompile(`"home\.limits\.l1b":\s*"((?:[^"\\]|\\.)*)"`)
	for _, lang := range []struct {
		name, block string
	}{{"zh-CN", zh}, {"en", en}} {
		m := claim.FindStringSubmatch(lang.block)
		if m == nil {
			t.Fatalf("%s has no home.limits.l1b; this fence no longer guards anything", lang.name)
		}
		if regexp.MustCompile(`\d`).MatchString(m[1]) {
			t.Errorf("%s home.limits.l1b contains a digit: %q\n"+
				"a count written into the claim goes stale the next time a record is added; "+
				"put it in home.limits.l1count, which /api/meta fills", lang.name, m[1])
		}
	}

	// The tally string exists in both languages and names both values.
	for _, lang := range []struct {
		name, block string
	}{{"zh-CN", zh}, {"en", en}} {
		for _, ph := range []string{`"home.limits.l1count":`, "{records}", "{guides}"} {
			if !strings.Contains(lang.block, ph) {
				t.Errorf("%s is missing %s in the corpus tally string", lang.name, ph)
			}
		}
	}

	// And the script reads the count from the server and substitutes both, so
	// the placeholders cannot reach a reader as literal braces.
	home := stripJSComments(asset(t, "home.js"))
	// /api/health, not /api/meta: the landing page's readers are not signed in,
	// and only health is outside the gate. Reading a gated endpoint here would
	// leave both sentences permanently hidden with a 401 in the console.
	for _, want := range []string{"/api/health", "corpus_opportunities", "corpus_knowledge_docs",
		`"{records}"`, `"{guides}"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home.js never mentions %s; the tally would render its own placeholders", want)
		}
	}
	// The elements are hidden by default, so a deployment whose /api/meta is
	// unreachable shows the limitations without a half-written sentence.
	html := asset(t, "index.html")
	for _, id := range []string{`id="corpusTally" hidden`, `id="liveStatus" hidden`} {
		if !strings.Contains(html, id) {
			t.Errorf("%s is missing from the markup; an unanswered /api/meta would leave an empty line", id)
		}
	}
}

// Whether the nationwide lookup is connected is a fact about a deployment.
//
// The section described it only as "configured separately", which stops being
// true the moment somebody configures it — and then the one part of the page
// whose whole job is honesty is the part that is out of date. The page reads
// live_search_enabled from the same /api/meta the app already reads for its own
// flag, so the front page and the conversation cannot disagree about it.
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
func TestLiveLookupStatusComesFromTheDeployment(t *testing.T) {
	zh, en := localeBlocks(t, asset(t, "i18n.js"))
	for _, lang := range []struct{ name, block string }{{"zh-CN", zh}, {"en", en}} {
		for _, key := range []string{`"home.limits.l4on":`, `"home.limits.l4off":`} {
			if !strings.Contains(lang.block, key) {
				t.Errorf("%s is missing %s; the page could only state one of the two states", lang.name, key)
			}
		}
	}
	home := stripJSComments(asset(t, "home.js"))
	for _, want := range []string{"live_search_enabled", "home.limits.l4on", "home.limits.l4off"} {
		if !strings.Contains(home, want) {
			t.Errorf("home.js never mentions %s; the live-lookup line could not follow the deployment", want)
		}
	}
}

// SAMPLE/ is a source_ref prefix, not the identifier a reader sees.
//
// The page claimed "编号以 SAMPLE/ 开头，这个前缀会一直显示到屏幕上". Half of that
// is true — every record's source_ref does begin with SAMPLE/ — but the id the
// answer quotes is `job-001` / `trn-002`, with no prefix, so the sentence
// described something the reader never sees. On the section headed 真话, a
// half-true sentence is the whole problem.
func TestSampleClaimDescribesTheSourceRefNotTheVisibleID(t *testing.T) {
	i18n := asset(t, "i18n.js")
	if strings.Contains(i18n, "这个前缀会一直显示到屏幕上") ||
		strings.Contains(i18n, "that prefix reaches the screen") {
		t.Error("the copy still claims the SAMPLE/ prefix reaches the screen; " +
			"answers quote the bare id (job-001), so it does not")
	}
	// Wherever SAMPLE/ is claimed, it is claimed of the source reference.
	for _, m := range regexp.MustCompile(`"([^"]+)":\s*"((?:[^"\\]|\\.)*SAMPLE/(?:[^"\\]|\\.)*)"`).
		FindAllStringSubmatch(i18n, -1) {
		key, val := m[1], m[2]
		if !strings.Contains(val, "依据编号") && !strings.Contains(val, "source reference") {
			t.Errorf("%s says SAMPLE/ without saying it is the source reference: %q", key, val)
		}
	}
}
