package guardrail

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// ToolCallRecord is what a verifier is allowed to know about a tool call.
//
// Meta is the important field: a tool declares facts about its own execution
// ("I suppressed 3 cells", "the caller supplied no owner") and verifiers read
// those, instead of parsing the tool's prose output with regexes. Parsing prose
// is how a check quietly stops working when a message is reworded.
type ToolCallRecord struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
	Err    string         `json:"error,omitempty"`
	Result string         `json:"-"`
}

// VerifyInput is everything the verify stage can see.
type VerifyInput struct {
	Intent string
	Answer string
	Role   string
	// Locale is the language the answer was supposed to be written in:
	// "zh-CN", "en", or "match" (no constraint).
	Locale      string
	AccessNeeds []string
	ToolCalls   []ToolCallRecord
	// KnownRef reports whether an identifier exists in the corpus.
	KnownRef func(string) bool
	// ConsentGranted reports whether the subject granted a scope.
	ConsentGranted func(scope string) bool
	// KFloor is the configured k-anonymity floor.
	KFloor int
}

// Verifier is one named output check. Names come from the intent registry, so
// adding a check to an intent is a one-line change in one file.
type Verifier func(VerifyInput) []Finding

var verifiers = map[string]Verifier{
	"citations_present":          verifyCitationsPresent,
	"no_eligibility_verdict":     verifyNoEligibilityVerdict,
	"actionable_next_step":       verifyActionableNextStep,
	"no_invented_identifiers":    verifyNoInventedIdentifiers,
	"plain_language":             verifyPlainLanguage,
	"offline_route_present":      verifyOfflineRoute,
	"no_cohort_downranking":      verifyNoCohortDownranking,
	"consent_on_file":            verifyConsentOnFile,
	"task_has_owner_and_channel": verifyTaskOwnerAndChannel,
	"no_silent_closure":          verifyNoSilentClosure,
	"k_anonymity":                verifyKAnonymity,
	"no_identifiers":             verifyNoIdentifiers,
	"coverage_stated":            verifyCoverageStated,
	"no_causal_overreach":        verifyNoCausalOverreach,
	"no_false_reassurance":       verifyNoFalseReassurance,
	"reply_language":             verifyReplyLanguage,
	"answers_the_city":           verifyAnswersTheCity,
}

// Verify runs the named checks in order and returns every finding.
// An unknown name is itself a finding: a typo in the intent registry must not
// silently disable a check.
func Verify(names []string, in VerifyInput) []Finding {
	var out []Finding
	for _, n := range names {
		v, ok := verifiers[n]
		if !ok {
			out = append(out, Finding{
				Guard: "verify", Code: "UNKNOWN_VERIFIER", Severity: Advisory,
				Message: fmt.Sprintf("Intent %q names verifier %q, which is not registered. The check did not run.", in.Intent, n),
			})
			continue
		}
		out = append(out, v(in)...)
	}
	return out
}

// VerifierNames lists everything registered, for the registry consistency test.
func VerifierNames() []string {
	out := make([]string, 0, len(verifiers))
	for n := range verifiers {
		out = append(out, n)
	}
	return out
}

// ---------------------------------------------------------------- primitives

var (
	// Every id prefix the corpus uses. `nat` was added with the national layer;
	// leaving it out here made a correctly-cited answer look uncited, which
	// forced a redraft and produced a worse answer than the one it replaced.
	refToken     = regexp.MustCompile(`\b(?:job|trn|ent|sub|kb|nat|live)-\d{3}\b`)
	sourceToken  = regexp.MustCompile(`\bSAMPLE/[A-Za-z0-9_\-/]+`)
	phoneToken   = regexp.MustCompile(`\b\d{3,4}-\d{4}-\d{4}\b|\b\d{3,4}-\d{7,8}\b|\b1[3-9]\d{9}\b|\b(?:12333|12345)\b`)
	urlToken     = regexp.MustCompile(`https?://\S+`)
	internalID   = regexp.MustCompile(`\b(?:sub|ses|task|appr)_\d{4}\b`)
	percentToken = regexp.MustCompile(`\d+(?:\.\d+)?\s*%`)
)

// retrievedSomething reports whether any retrieval tool came back with results,
// read from the tool's own declared Meta rather than from its prose.
func retrievedSomething(in VerifyInput) bool {
	for _, c := range in.ToolCalls {
		switch c.Name {
		case "opportunity_search", "knowledge_search":
			if c.Err == "" {
				if n, ok := metaInt(c, "result_count"); ok && n > 0 {
					return true
				}
			}
		case "criteria_explain":
			if c.Err == "" {
				return true
			}
		}
	}
	return false
}

func ranTool(in VerifyInput, names ...string) bool {
	for _, c := range in.ToolCalls {
		for _, n := range names {
			if c.Name == n && c.Err == "" {
				return true
			}
		}
	}
	return false
}

func metaInt(c ToolCallRecord, key string) (int, bool) {
	v, ok := c.Meta[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func metaBool(c ToolCallRecord, key string) bool {
	b, _ := c.Meta[key].(bool)
	return b
}

// ------------------------------------------------------------------- checks

// verifyCitationsPresent: if the turn retrieved anything, the answer has to show
// where it came from. Without this the corpus is decoration.
func verifyCitationsPresent(in VerifyInput) []Finding {
	// Only fires when retrieval actually returned something. A search that found
	// nothing has nothing to cite, and demanding a citation there would push the
	// model to invent one - the exact failure the next verifier exists to catch.
	if !retrievedSomething(in) {
		return nil
	}
	if refToken.MatchString(in.Answer) || sourceToken.MatchString(in.Answer) {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "MISSING_CITATION", Severity: Repair,
		Message: "The answer describes retrieved programs but names no record, so the reader cannot check any of it.",
		Remedy:  "Name each program you used by its id (for example job-001, sub-003) so the reader can look it up.",
	}}
}

// verdictPatterns are the phrasings that turn a criteria checklist into a
// decision. The agent may report what the published criteria say and what looks
// met; the decision belongs to the issuing authority.
var verdictPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\byou (are|'re) (definitely |certainly |clearly )?(not )?eligible\b`),
	regexp.MustCompile(`(?i)\byou (do not|don't|will not|won't) qualify\b`),
	regexp.MustCompile(`(?i)\byou qualify\b`),
	regexp.MustCompile(`(?i)\byour application (will|would) be (approved|rejected|refused)\b`),
	regexp.MustCompile(`(?i)\bi (can )?confirm (that )?you\b.{0,30}\b(qualify|eligible)\b`),
	regexp.MustCompile(`(?i)(你|您)(完全|肯定|一定)?(不)?符合(条件|资格)`),
	regexp.MustCompile(`(?i)(你|您)(肯定|一定)(能|不能)(领|申请)`),
}

var reviewDisclaimer = regexp.MustCompile(`(?i)(official review|the (issuing )?(authority|window|bureau) decides|subject to review|only the .{0,30}(authority|window|bureau) can|以(受理|审核)(窗口|机构)(的)?(认定|结论)为准|最终以.{0,10}(审核|认定)为准)`)

func verifyNoEligibilityVerdict(in VerifyInput) []Finding {
	var hits []string
	for _, re := range verdictPatterns {
		if m := re.FindString(in.Answer); m != "" {
			hits = append(hits, m)
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sev := Repair
	msg := "The answer states an eligibility decision. Only the issuing authority decides eligibility; stating it here is the failure mode this product exists to avoid."
	if reviewDisclaimer.MatchString(in.Answer) {
		// A hedge nearby makes it less dangerous but still wrong: the sentence
		// the person quotes to a counter is the unhedged one.
		msg = "The answer states an eligibility decision, even though it also mentions official review. The decisive sentence is the one that will be repeated at the counter."
	}
	return []Finding{{
		Guard: "verify", Code: "ELIGIBILITY_VERDICT", Severity: sev,
		Message: msg, Evidence: hits,
		Remedy: "Rewrite as a checklist: for each published criterion say met, unmet or unknown, name the document that proves it, " +
			"and state that the issuing authority makes the decision.",
	}}
}

// verifyActionableNextStep: an answer that ends without a way to act is a
// conversation, not a bridge.
func verifyActionableNextStep(in VerifyInput) []Finding {
	if phoneToken.MatchString(in.Answer) || urlToken.MatchString(in.Answer) ||
		containsAny(in.Answer, "window", "service hall", "counter", "opening hours", "窗口", "服务大厅", "办事") ||
		ranTool(in, "case_task_create", "handoff_to_human", "document_prepare") {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "NO_NEXT_STEP", Severity: Repair,
		Message: "The answer gives no way to act: no link, no phone number, no service window, and no task created.",
		Remedy:  "End with exactly one next action and attach its channel: a link, a phone number, or an address with opening hours.",
	}}
}

// verifyNoInventedIdentifiers: every program id or source reference in the
// answer must exist in the corpus. This is the check that stops a fluent
// invention from reaching somebody who will act on it.
func verifyNoInventedIdentifiers(in VerifyInput) []Finding {
	if in.KnownRef == nil {
		return nil
	}
	var bad []string
	seen := map[string]bool{}
	tokens := append(refToken.FindAllString(in.Answer, -1), sourceToken.FindAllString(in.Answer, -1)...)
	for _, tok := range tokens {
		tok = strings.TrimRight(tok, ".,;:)")
		if seen[tok] {
			continue
		}
		seen[tok] = true
		if !in.KnownRef(tok) {
			bad = append(bad, tok)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "INVENTED_IDENTIFIER", Severity: Block,
		Message:  "The answer names identifiers that are not in the corpus. Somebody could try to use these at a counter.",
		Evidence: bad,
		Remedy:   "Only name programs returned by a search this turn. If nothing matched, say so and offer the human channel.",
	}}
}

// verifyAnswersTheCity holds the line that national coverage is coverage.
//
// National programmes are administered locally, so somebody in 深圳 can act on
// them in 深圳. An answer that never names their city — or worse, opens with
// "这边我没有本地清单" — tells them there is nothing for them, which is false and
// is exactly the failure this check exists to catch. If a search was run for a
// city and anything came back, the answer has to be written for that city.
func verifyAnswersTheCity(in VerifyInput) []Finding {
	// Only fires when every result was national — that is the case at risk.
	// Where local listings came back, the answer already names a district, an
	// employer and a street, and demanding the city name on top would be
	// pedantry that costs a redraft.
	var city string
	var names []string
	var national, local int
	for _, c := range in.ToolCalls {
		if c.Name != "opportunity_search" || c.Err != "" {
			continue
		}
		if v, _ := c.Meta["asked_city"].(string); v != "" {
			city = v
		}
		if v, _ := c.Meta["asked_city_names"].([]string); len(v) > 0 {
			names = v
		}
		n, _ := metaInt(c, "result_count")
		l, _ := metaInt(c, "local_hits")
		national += n - l
		local += l
	}
	if city == "" || local > 0 || national == 0 {
		return nil
	}
	// Any spelling counts: an English answer writes "Chengdu" where the corpus
	// says 成都, and that is the city being named, not a miss.
	if len(names) == 0 {
		names = []string{city}
	}
	low := strings.ToLower(in.Answer)
	for _, n := range names {
		if strings.Contains(low, strings.ToLower(n)) {
			return nil
		}
	}
	return []Finding{{
		Guard: "verify", Code: "CITY_NOT_ANSWERED", Severity: Repair,
		Message: fmt.Sprintf("The search was run for %s and returned programmes that apply there, "+
			"but the answer never names %s.", city, city),
		Remedy: fmt.Sprintf("Write the answer for %s. Say what the person can do in %s, name that city's "+
			"own 12333, and do not open with what the corpus lacks.", city, city),
	}}
}

// verifyReplyLanguage checks that the answer is actually written in the language
// the person was promised.
//
// It exists because "answer in Chinese" is a prompt instruction, and every
// pressure in the request pushes the other way: the charter, the persona, the
// tool descriptions and the whole sample corpus are English. An answer in the
// wrong language is not a style slip here - it is an answer the person cannot
// use, delivered by a service whose entire purpose is removing that barrier.
//
// The measure is deliberately crude and explainable: which script carries more
// of the answer. That tolerates the carve-outs - an answer in Chinese quoting an
// English address and a programme id still passes comfortably.
func verifyReplyLanguage(in VerifyInput) []Finding {
	if in.Locale == "" || in.Locale == "match" {
		return nil
	}
	cjk, latin := scriptCounts(in.Answer)
	if cjk+latin < 12 {
		return nil // too short to judge; a one-line refusal is not a language failure
	}
	want := "Chinese"
	wrong := false
	switch {
	case strings.HasPrefix(in.Locale, "zh"):
		wrong = cjk < latin
	case strings.HasPrefix(in.Locale, "en"):
		want, wrong = "English", cjk > latin
	default:
		return nil
	}
	if !wrong {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "WRONG_REPLY_LANGUAGE", Severity: Repair,
		Message: fmt.Sprintf("The answer was to be written in %s, but it is mostly not "+
			"(%d Chinese characters against %d Latin words).", want, cjk, latin),
		Remedy: fmt.Sprintf("Rewrite the whole answer in %s. Keep programme ids, phone numbers, "+
			"addresses and opening hours exactly as the tools returned them.", want),
	}}
}

// scriptCounts returns Han characters and Latin words, ignoring digits and
// punctuation so that a phone number or a programme id counts as neither.
func scriptCounts(s string) (cjk, latin int) {
	inWord := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			cjk++
			inWord = false
		case unicode.IsLetter(r):
			if !inWord {
				latin++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	return cjk, latin
}

var jargon = map[string]string{
	"eligibility criteria":  "what they ask for",
	"remuneration":          "pay",
	"prerequisite":          "what you need first",
	"statutory":             "required by law",
	"pursuant to":           "under",
	"submit an application": "apply",
	"aforementioned":        "the one above",
	"utilise":               "use",
	"commence":              "start",
	"in accordance with":    "following",
}

// verifyPlainLanguage measures what it claims to measure: sentence length and
// jargon count. It does not try to score "readability" in the abstract, because
// a number nobody can act on is worse than two numbers they can.
func verifyPlainLanguage(in VerifyInput) []Finding {
	var out []Finding
	sentences := splitSentences(in.Answer)
	var longest, total, n int
	for _, s := range sentences {
		w := countUnits(s)
		if w > longest {
			longest = w
		}
		total += w
		n++
	}
	if n > 0 {
		// Thresholds are per script, because a "unit" is not the same size in
		// each. Twenty English words and twenty Chinese characters are very
		// different sentences, and one threshold for both flagged every
		// perfectly readable Chinese answer.
		avgCap, longCap, unit := 22, 40, "words"
		if cjk, latin := scriptCounts(in.Answer); cjk > latin {
			avgCap, longCap, unit = 30, 48, "characters"
		}
		avg := total / n
		if avg > avgCap || longest > longCap {
			out = append(out, Finding{
				Guard: "verify", Code: "SENTENCES_TOO_LONG", Severity: Repair,
				Message: fmt.Sprintf("Average sentence length is %d %s and the longest is %d (limits: %d and %d). "+
					"This intent serves people for whom a long sentence is a reason to stop reading.",
					avg, unit, longest, avgCap, longCap),
				Remedy: "Rewrite with one idea per sentence. Under 20 words, or under 30 Chinese characters.",
			})
		}
	}
	low := strings.ToLower(in.Answer)
	var found []string
	for term, plain := range jargon {
		if strings.Contains(low, term) {
			found = append(found, term+" -> "+plain)
		}
	}
	if len(found) > 0 {
		out = append(out, Finding{
			Guard: "verify", Code: "JARGON_PRESENT", Severity: Repair,
			Message: "The answer uses administrative vocabulary where an everyday word exists.", Evidence: found,
			Remedy: "Replace each term with the plain equivalent shown, or define it once in brackets.",
		})
	}
	return out
}

func verifyOfflineRoute(in VerifyInput) []Finding {
	if phoneToken.MatchString(in.Answer) ||
		containsAny(in.Answer, "window", "service hall", "counter", "opening hours", "窗口", "服务大厅", "现场") ||
		ranTool(in, "handoff_to_human") {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "NO_OFFLINE_ROUTE", Severity: Repair,
		Message: "Only an online route was offered. For this intent that is the route most likely to fail.",
		Remedy:  "Add a phone number, or an address with opening hours, taken from the record's channel field.",
	}}
}

var cohortWords = []string{
	"migrant worker", "graduate", "gig worker", "flexible worker", "caregiver",
	"older worker", "disabled", "disability", "农民工", "毕业生", "灵活就业", "照护", "大龄", "残障",
}

var exclusionWords = []string{
	"not suitable", "cannot apply", "should not apply", "unlikely to be considered",
	"not for you", "won't be hired", "not worth applying", "不适合", "不用申请", "别申请", "不会要你",
}

// verifyNoCohortDownranking: a cohort tag exists to add support. The moment it
// is used to remove an option, the product has become the thing it was built to
// replace.
func verifyNoCohortDownranking(in VerifyInput) []Finding {
	var hits []string
	for _, s := range splitSentences(in.Answer) {
		low := strings.ToLower(s)
		if containsAnyLower(low, cohortWords) && containsAnyLower(low, exclusionWords) {
			hits = append(hits, strings.TrimSpace(s))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "COHORT_DOWNRANKING", Severity: Block,
		Message:  "The answer uses the person's situation as a reason not to pursue an option.",
		Evidence: hits,
		Remedy: "State the published criterion and what it would take to meet it. Never tell somebody their group makes an option " +
			"not worth trying; if a criterion genuinely blocks it, name the criterion.",
	}}
}

// recordTouchingTools are the tools that read or write a named resident's
// record. Consent is checked against what the turn actually did, not against the
// intent in the abstract: a turn that only explains what permission is needed,
// and offers to ask for it, is the correct behaviour and must not be blocked.
var recordTouchingTools = []string{
	"case_task_create", "case_task_update", "case_task_list", "document_prepare", "profile_upsert",
}

func verifyConsentOnFile(in VerifyInput) []Finding {
	if in.ConsentGranted == nil || in.ConsentGranted("share_with_caseworker") {
		return nil
	}
	var touched []string
	for _, c := range in.ToolCalls {
		if c.Err != "" {
			continue
		}
		for _, name := range recordTouchingTools {
			if c.Name == name {
				touched = append(touched, name)
			}
		}
	}
	if len(touched) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "CONSENT_MISSING", Severity: Block,
		Message:  "This turn acted on a resident's record, and no share_with_caseworker consent is on file.",
		Evidence: touched,
		Remedy:   "Do not read or write the resident's record. Explain what consent is needed and offer to request it with consent_request.",
	}}
}

func verifyTaskOwnerAndChannel(in VerifyInput) []Finding {
	var bad []string
	for _, c := range in.ToolCalls {
		if c.Name != "case_task_create" || c.Err != "" {
			continue
		}
		if metaBool(c, "missing_owner") || metaBool(c, "missing_channel") {
			title, _ := c.Args["title"].(string)
			bad = append(bad, title)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "TASK_INCOMPLETE", Severity: Repair,
		Message:  "A task was created with no owner or no channel. A task nobody owns is a task the resident ends up owning by default.",
		Evidence: bad,
		Remedy:   "Update each task with an owner and the channel (link, phone, or window with hours) before answering.",
	}}
}

func verifyNoSilentClosure(in VerifyInput) []Finding {
	var bad []string
	for _, c := range in.ToolCalls {
		if c.Name != "case_task_update" || c.Err != "" {
			continue
		}
		if metaBool(c, "closed_without_evidence") {
			id, _ := c.Args["task_id"].(string)
			bad = append(bad, id)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "SILENT_CLOSURE", Severity: Block,
		Message:  "A task was marked done with no evidence of the underlying step.",
		Evidence: bad,
		Remedy:   "Set the task to waiting or blocked with the blocker named, or supply the evidence that the step actually happened.",
	}}
}

func verifyKAnonymity(in VerifyInput) []Finding {
	var out []Finding
	ran := false
	for _, c := range in.ToolCalls {
		if c.Name != "gap_analysis" || c.Err != "" {
			continue
		}
		ran = true
		if n, ok := metaInt(c, "suppressed_cells"); ok && n > 0 {
			if !containsAny(in.Answer, "suppress", "withheld", "too small", "below the floor", "不予披露", "样本过小") {
				out = append(out, Finding{
					Guard: "verify", Code: "SUPPRESSION_NOT_DISCLOSED", Severity: Repair,
					Message: fmt.Sprintf("%d cell(s) were suppressed for being below the anonymity floor of %d, and the answer does not say so.", n, in.KFloor),
					Remedy:  "State how many breakdowns were withheld and why. Do not re-slice to get under the floor.",
				})
			}
		}
	}
	if !ran && containsAny(in.Answer, "%", "respondents", "people in", "记录", "人次") {
		out = append(out, Finding{
			Guard: "verify", Code: "UNSOURCED_AGGREGATE", Severity: Block,
			Message: "The answer reports figures but gap_analysis did not run, so no anonymity floor was applied to them.",
			Remedy:  "Every number in this intent must come from gap_analysis. Run it, or report no figures.",
		})
	}
	return out
}

func verifyNoIdentifiers(in VerifyInput) []Finding {
	var out []Finding
	if f := HasPII(in.Answer); len(f) > 0 {
		out = append(out, Finding{
			Guard: "verify", Code: "PII_IN_AGGREGATE", Severity: Block,
			Message: "The answer contains a personal identifier. This intent reports on populations only.",
			Remedy:  "Remove the identifier. If the question needs it, the question is out of scope for this intent.",
		})
	}
	if m := internalID.FindAllString(in.Answer, -1); len(m) > 0 {
		out = append(out, Finding{
			Guard: "verify", Code: "INTERNAL_ID_LEAKED", Severity: Block,
			Message:  "The answer contains internal record ids, which can be joined back to individuals.",
			Evidence: m,
			Remedy:   "Report counts and rates only.",
		})
	}
	return out
}

func verifyCoverageStated(in VerifyInput) []Finding {
	if !ranTool(in, "gap_analysis") {
		return nil
	}
	if percentToken.MatchString(in.Answer) &&
		containsAny(in.Answer, "coverage", "consent", "覆盖", "授权") {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "COVERAGE_NOT_STATED", Severity: Repair,
		Message: "A figure was reported without saying what share of the population consented to be counted.",
		Remedy:  "State the consent coverage percentage next to the figure, and say plainly that low coverage makes it a hypothesis.",
	}}
}

// reassurancePhrases are the sentences that make an answer feel kinder without
// making it truer. They are forbidden by the persona, and checked here because a
// persona is a prompt and a prompt is not a control.
//
// A warm sentence that sends somebody to a counter for nothing is not kindness,
// it is an unpaid trip across a city. The replacement is always the same shape:
// what is known, what is not, and who decides.
var reassurancePhrases = []string{
	"don't worry", "do not worry", "no need to worry", "nothing to worry",
	"rest assured", "it'll be fine", "it will be fine", "you'll be fine",
	"you will be fine", "no problem at all", "everything will work out",
	"别担心", "不用担心", "不必担心", "放心", "肯定没问题", "一定没问题", "包在我身上", "没事的",
}

func verifyNoFalseReassurance(in VerifyInput) []Finding {
	var hits []string
	for _, s := range splitSentences(in.Answer) {
		if containsAnyLower(strings.ToLower(s), reassurancePhrases) {
			hits = append(hits, strings.TrimSpace(s))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "FALSE_REASSURANCE", Severity: Repair,
		Message:  "The answer reassures instead of informing. Comfort that is not backed by a fact is what sends people to a counter for nothing.",
		Evidence: hits,
		Remedy: "Delete the reassurance. Put in its place what is known, what is not known, " +
			"and who makes the decision.",
	}}
}

var causalWords = []string{" because ", " caused by ", " causes ", " leads to ", " due to ", "导致", "造成", "是因为"}

func verifyNoCausalOverreach(in VerifyInput) []Finding {
	var hits []string
	for _, s := range splitSentences(in.Answer) {
		low := " " + strings.ToLower(s) + " "
		if containsAnyLower(low, causalWords) && (percentToken.MatchString(s) || hasDigit(s)) {
			hits = append(hits, strings.TrimSpace(s))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return []Finding{{
		Guard: "verify", Code: "CAUSAL_OVERREACH", Severity: Repair,
		Message:  "A causal claim was attached to a count. These data show co-occurrence, not cause.",
		Evidence: hits,
		Remedy:   "Rewrite as association, and name at least one confound you can see in the data.",
	}}
}

// ------------------------------------------------------------------ helpers

func splitSentences(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' || r == '。' || r == '！' || r == '？' {
			if t := strings.TrimSpace(cur.String()); t != "" {
				out = append(out, t)
			}
			cur.Reset()
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

// countUnits counts Latin words plus CJK characters, so one threshold works for
// both scripts without pretending a Chinese sentence has "words".
func countUnits(s string) int {
	n := 0
	inWord := false
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
			inWord = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inWord {
				n++
				inWord = true
			}
		} else {
			inWord = false
		}
	}
	return n
}

func containsAny(s string, subs ...string) bool {
	return containsAnyLower(strings.ToLower(s), subs)
}

func containsAnyLower(low string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(low, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
