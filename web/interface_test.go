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

// A delivery toggle must be able to turn its setting OFF, and must show what is
// actually in force.
//
// Both halves failed, and together they made a trap: the change handler sent a
// message only when the box was ticked, so the control that switched plain
// language on could not switch it off; and `.checked` was read but never
// written, so after a reload the box sat empty while the mode was still on. The
// person was left with a setting they could not clear and a control that denied
// it was set — on the one feature built for people who cannot easily read the
// screen in the first place.
//
// The only escape was to say so in words, or to lose the whole profile to
// 清空记录. Neither is signposted anywhere.
// See docs/bugfix/2026-08-28-plain-language-could-not-be-turned-off.md
func TestPlainLanguageCanBeTurnedOffAndShowsWhatIsInForce(t *testing.T) {
	src := asset(t, "app.js")

	start := strings.Index(src, `$("#a11yPlain").addEventListener`)
	if start < 0 {
		t.Fatal("the plain-language handler is gone; this fence no longer guards anything")
	}
	end := strings.Index(src[start:], `$("#theme")`)
	if end < 0 {
		t.Fatal("could not find the end of the handler")
	}
	handler := src[start : start+end]

	// An else branch is the whole point: ticking sends one message, unticking
	// must send the opposite one.
	if !strings.Contains(handler, "} else {") {
		t.Error("the plain-language handler has no off branch: unticking the box does nothing, " +
			"so the setting cannot be switched off from the interface")
	}
	if strings.Count(handler, "send(") < 2 {
		t.Errorf("the handler sends %d message(s); it needs one for on and one for off",
			strings.Count(handler, "send("))
	}
	// Reflecting the state must not itself look like the person asking for it.
	if !strings.Contains(handler, "if (state.syncingA11y) return;") {
		t.Error("the handler does not ignore changes it made itself: reflecting the server's state " +
			"would send a message on every refresh")
	}

	if !strings.Contains(src, "function reflectDeliverySettings(") {
		t.Fatal("reflectDeliverySettings is gone; the checkbox no longer shows what is in force")
	}
	reflect := section(t, src, "function reflectDeliverySettings(")
	if !strings.Contains(reflect, "access_needs") || !strings.Contains(reflect, "plain_language") {
		t.Error("reflectDeliverySettings does not read the session's access_needs")
	}
	if !strings.Contains(reflect, "box.checked = on") {
		t.Error("reflectDeliverySettings never writes .checked, so the box still cannot show the truth")
	}
	// It has to actually run after a refresh, or it guards nothing.
	if !strings.Contains(src, "reflectDeliverySettings(d.session)") {
		t.Error("reflectDeliverySettings is never called from the overview refresh")
	}
}

// Stored delivery settings must reach the reader as words, not as enum values.
//
// The records panel printed "plain_language" at somebody whose stated need is
// to not be shown vocabulary like that. Every value in the enum needs a label in
// both languages; a missing one falls back to the raw id, so this checks the
// table rather than the fallback.
func TestEveryDeliverySettingHasAReaderFacingLabel(t *testing.T) {
	app := asset(t, "app.js")
	if !strings.Contains(app, `term("need", n, n)`) {
		t.Error("the records panel does not render access needs through term(): " +
			"it prints the raw enum value at the reader")
	}

	i18n := asset(t, "i18n.js")
	// The six values in domain.AccessNeed. Listed here rather than derived,
	// because a value added to the Go enum with no label is exactly the silent
	// gap this test exists to catch.
	for _, need := range []string{
		"plain_language", "large_text", "voice", "dialect", "assisted", "low_bandwidth",
	} {
		key := `"need.` + need + `"`
		if n := strings.Count(i18n, key); n < 2 {
			t.Errorf("%s appears %d time(s) in the TERMS table; it needs one per language, "+
				"or that reader sees the raw id", key, n)
		}
	}
}
