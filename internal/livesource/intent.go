package livesource

import (
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

// Intent is what the person wants from the open web.
//
// ── Why this type exists ─────────────────────────────────────────────────────
//
// Every live lookup used to search for 招聘 and then discard anything that did
// not read as a job advertisement. That was correct for the case it was built
// for and silently wrong for the other half of the product: somebody asking
// where to LEARN a trade got recruitment adverts, or nothing at all. The corpus
// names courses in two cities, so outside those two the answer to 培训 was an
// empty result that reads to the reader as "there is nothing for you here" —
// the one lie this whole module exists to prevent.
//
// The fix is not a wider word list. A single filter that accepts both job ads
// and course pages would hand job seekers course adverts, which is the noise the
// 2026-08-28 change was written to remove. What was missing is that the caller
// KNOWS which of the two it wants, and had no way to say so.
//
// See docs/bugfix/2026-08-28-live-search-never-looked-for-training.md.
type Intent string

const (
	// IntentWork is somebody looking for a job.
	IntentWork Intent = "work"
	// IntentTraining is somebody looking for a course, a place on one, or the
	// qualification it leads to.
	IntentTraining Intent = "training"
)

// searchableIntents is every intent the open web can be asked about, in the
// order results are labelled when a page satisfies more than one.
//
// Order matters only for that tie, and work comes first because a page that
// advertises both a course and the job at the end of it is, to somebody reading
// it, a job advert with training attached.
var searchableIntents = []Intent{IntentWork, IntentTraining}

// intentProfile is everything that differs between one intent and another.
//
// A table rather than branches, because these three things must stay in step:
// searching for 培训 while accepting only 招聘 pages returns nothing, and
// accepting course pages while warning about job scams tells the reader to
// watch for the wrong thing. Adding a third intent later is a row here, not a
// new `if` in three functions.
type intentProfile struct {
	// Term is added to the query when the caller's own words do not already say
	// what kind of thing they are after. The agent passes the trade, not the
	// intent — a real turn sent query="保洁" — so without this the search is
	// literally "深圳 保洁" and returns whatever the city is in the news for.
	Term string

	// Accept are the words a page of this kind uses. A page that uses none of
	// them is not that kind of page, whatever else it is about.
	//
	// A list rather than a cleverer relevance model because the failure it
	// prevents is gross, not subtle, and because a list can be read and
	// corrected by somebody who knows the domain and not this code.
	Accept []string

	// Caveat travels with the result to the model and on to the reader. It is
	// per-intent because the frauds are different: a job lead that asks for
	// money up front is the danger on one side, and 培训贷 — a "course" sold on
	// credit, with a promised certificate and a promised job — is the danger on
	// the other. Warning about the wrong one is worse than not warning.
	Caveat string
}

var intentProfiles = map[Intent]intentProfile{
	IntentWork: {
		Term:   "招聘",
		Accept: recruitmentWords,
		Caveat: "这是网上搜到的线索，不是本系统核实过的岗位，发布网站多为商业招聘平台。" +
			"点开先看发布日期和单位名称；凡是先收费、先交押金、先办贷款的，都不要答应。" +
			"拿不准就打 12333 核一下再跑一趟。",
	},
	IntentTraining: {
		Term:   "培训",
		Accept: trainingWords,
		Caveat: "这是网上搜到的线索，不是本系统核实过的课程，发布网站多为商业培训机构。" +
			"点开先看办学资质和发布日期；凡是承诺包过、包证、包分配的，或者要先交学费、办培训贷、办分期的，都不要答应。" +
			"能报销学费的补贴性培训有目录，以当地人社部门公布的为准，拿不准就打 12333 核一下再报名。",
	},
}

// recruitmentWords are the words a page about hiring uses.
//
// Measured 2026-08-28 on "深圳 保洁": 4 of 20 results looked like recruitment
// without the 招聘 term; with it, 16 of 17.
var recruitmentWords = []string{
	"招聘", "急聘", "诚聘", "招工", "招人", "用工", "直聘",
	"求职", "人才网", "兼职", "岗位", "职位",
	"月薪", "薪资", "工资", "待遇", "包吃住", "日结", "周结", "月结",
}

// trainingWords are the words a page about learning a trade uses: the class,
// the enrolment, the money and the certificate at the end.
//
// !! NOT MEASURED AGAINST THE LIVE INDEX. recruitmentWords above carries a
// count taken from real Bocha responses; this list was written from the
// vocabulary these pages use and is fenced only against fixtures, because no
// search key was available when it was added. The first live run should count
// how many course pages it accepts and rejects, the same way, and correct it.
var trainingWords = []string{
	"培训", "招生", "开班", "报名", "课程", "学费", "学时", "学制",
	"实训", "结业", "技能提升", "职业技能", "职业资格", "技能等级",
	"考证", "资格证", "技工学校", "技师学院", "职业院校", "职业学校",
}

// intentForKind maps the corpus's own vocabulary onto this package's.
//
// It is an explicit table and not a string comparison, even though
// domain.KindTraining and IntentTraining happen to spell the same word today:
// domain.KindJob is "job" and the intent is "work", so a comparison would have
// silently mapped a job search onto nothing at all — which is the shape of the
// defect this whole file exists to fix.
//
// Subsidy and entrepreneurship have no row, deliberately. They are administered
// by an authority rather than advertised as pages somebody can turn up to, so
// the honest live answer for them is the official directory entry this chain
// already returns, not a commercial page about money you can supposedly claim.
var intentForKind = map[string]Intent{
	string(domain.KindJob):      IntentWork,
	string(domain.KindTraining): IntentTraining,
}

// IntentsFor maps what the caller asked the corpus for onto what the open web
// can be asked for.
//
// A caller who asks ONLY for the kinds with no row gets no narrowing rather than
// an empty search: somebody chasing a training subsidy is usually also after the
// course it pays for. Likewise an empty or unrecognised list means "everything
// searchable". A forgotten field must WIDEN the search, never silence it —
// silence would reach the person as "there is nothing in your city", and that is
// the failure this module exists to prevent.
func IntentsFor(kinds []string) []Intent {
	asked := map[Intent]bool{}
	for _, k := range kinds {
		if in, ok := intentForKind[strings.TrimSpace(k)]; ok {
			asked[in] = true
		}
	}
	var out []Intent
	for _, want := range searchableIntents {
		if asked[want] {
			out = append(out, want)
		}
	}
	if len(out) == 0 {
		return append([]Intent(nil), searchableIntents...)
	}
	return out
}

// intents returns what this query asked for, defaulting to everything.
func (q Query) intents() []Intent {
	if len(q.Intents) == 0 {
		return append([]Intent(nil), searchableIntents...)
	}
	out := make([]Intent, 0, len(q.Intents))
	for _, want := range searchableIntents {
		for _, got := range q.Intents {
			if got == want {
				out = append(out, want)
				break
			}
		}
	}
	if len(out) == 0 {
		return append([]Intent(nil), searchableIntents...)
	}
	return out
}

// intentQuery makes sure the search actually asks for the KIND of thing wanted,
// and does not ask twice when the caller already said it.
func intentQuery(city, terms string, in Intent) string {
	q := strings.TrimSpace(city + " " + terms)
	if !matchesIntent(q, in) {
		q += " " + intentProfiles[in].Term
	}
	return q
}

// matchesIntent reports whether a page reads as this kind of page.
func matchesIntent(text string, in Intent) bool {
	for _, w := range intentProfiles[in].Accept {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// tradeNeedles are the caller's words with the intent vocabulary taken out.
//
// Why the removal is not cosmetic: the needles exist to keep the result about
// the WORK the person asked about, and the intent filter already enforces the
// kind of page. Leaving 培训 in the needle list for a query like "养老护理 培训"
// would let any course page satisfy the trade test simply by being a course
// page — the trade filter would be doing nothing at all. Taking it out leaves
// 养老护理, which is the part that has to be on the page.
//
// A caller whose every word is intent vocabulary (query="培训") is left with no
// needles, which imposes nothing: they named no trade, so the city's courses are
// the right answer.
func tradeNeedles(terms string, intents []Intent) []string {
	var out []string
	for _, w := range strings.Fields(terms) {
		isIntentWord := false
		for _, in := range intents {
			if matchesIntent(w, in) {
				isIntentWord = true
				break
			}
		}
		if !isIntentWord {
			out = append(out, w)
		}
	}
	return out
}
