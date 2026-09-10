package leadgraph_test

// P6 + P9 fences for docs/20-lead-graph.zh-CN.md §05.3 路径二, §07 合规红线.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// fakeSource records whether it was ever asked, which is the only way to prove
// a refusal happened BEFORE the request rather than after it.
type fakeSource struct {
	name   string
	hosts  []string
	docs   []leadgraph.Document
	called int
}

func (f *fakeSource) Name() string    { return f.name }
func (f *fakeSource) Hosts() []string { return f.hosts }
func (f *fakeSource) Fetch(context.Context, leadgraph.FetchRequest) ([]leadgraph.Document, error) {
	f.called++
	return f.docs, nil
}

var day = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)

func at(d int) *time.Time { t := day.AddDate(0, 0, d); return &t }

// §07 — a forbidden host is refused before one request goes out. Refusing only
// at storage is too late: by then the scraping happened and the log on the
// other side exists.
func TestAForbiddenSourceIsNeverCalled(t *testing.T) {
	src := &fakeSource{name: "maimai", hosts: []string{"maimai.cn"}}
	docs, refused, err := leadgraph.FetchFrom(context.Background(), src, leadgraph.FetchRequest{}, day)

	if src.called != 0 {
		t.Fatalf("a forbidden source was contacted %d times", src.called)
	}
	if err == nil || !strings.Contains(err.Error(), "SOURCE_FORBIDDEN") {
		t.Errorf("want SOURCE_FORBIDDEN, got %v", err)
	}
	if len(docs) != 0 || len(refused) != 1 || refused[0].Host != "maimai.cn" {
		t.Errorf("the refusal was not recorded usefully: %+v", refused)
	}
}

// §07 — defence in depth: an allowed source that returns a forbidden URL still
// loses that document.
func TestAForbiddenURLInResultsIsDropped(t *testing.T) {
	src := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting, Org: "A司", Unit: "c业务组", Title: "招聘"},
		{URL: "https://www.linkedin.com/in/x", Kind: leadgraph.DocJobPosting, Org: "A司", Unit: "c业务组", Title: "简历"},
	}}
	docs, refused, err := leadgraph.FetchFrom(context.Background(), src, leadgraph.FetchRequest{}, day)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(docs) != 1 || !strings.Contains(docs[0].URL, "careers.example.com") {
		t.Fatalf("wrong documents survived: %+v", docs)
	}
	if len(refused) != 1 || !strings.Contains(refused[0].URL, "linkedin") {
		t.Errorf("the dropped document was not recorded: %+v", refused)
	}
}

// §05.3 — a registry filing is the company speaking, so it is Announced and
// outranks every rumour on the same record.
func TestRegistryChangeBecomesAnAnnouncedEvent(t *testing.T) {
	got := leadgraph.ExtractSignals([]leadgraph.Document{{
		URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
		Org: "A司", Unit: "c业务组", Title: "c业务组并入b业务组", Text: "登记变更",
		PostedAt: at(-1), FetchedAt: day,
	}})
	if len(got) != 1 || got[0].Kind != leadgraph.KindEvent {
		t.Fatalf("want one event, got %+v", got)
	}
	if got[0].Corroboration() != leadgraph.Announced {
		t.Errorf("a registry filing is not official? got %s", got[0].Corroboration())
	}
	if got[0].Intel[0].Excerpt == "" || got[0].Intel[0].SourceURL == "" {
		t.Errorf("the event cannot be checked against its source: %+v", got[0].Intel)
	}
	if got[0].OccurredAt == nil {
		t.Error("an event with no date cannot sit on a timeline")
	}
}

// §05.3 — a job advert says a group exists and is hiring. It does not say
// anything HAPPENED, so it must not manufacture an event.
func TestJobPostingProducesAUnitNotAnEvent(t *testing.T) {
	got := leadgraph.ExtractSignals([]leadgraph.Document{{
		URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting,
		Org: "A司", Unit: "c业务组", Title: "招后端", FetchedAt: day,
	}})
	if len(got) != 1 || got[0].Kind != leadgraph.KindUnit {
		t.Fatalf("a job advert produced %+v", got)
	}
	if got[0].Corroboration() == leadgraph.Announced {
		t.Error("a job advert was treated as an official statement")
	}
}

// §05.3 / §07 — the ledger keeps documents that produced nothing. "We looked
// and there was nothing" and "we never looked" are different answers.
func TestLedgerKeepsDocumentsThatProducedNothing(t *testing.T) {
	s := newStore(t)
	src := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/9", Kind: leadgraph.DocJobPosting, Org: "A司", Title: "无部门信息", FetchedAt: day},
	}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(rep.Applied) != 0 {
		t.Fatalf("a unit-less advert produced records: %+v", rep.Applied)
	}
	obs := s.Observations(amy, "A司")
	if len(obs) != 1 {
		t.Fatalf("the fruitless fetch left no trace: %+v", obs)
	}
	if len(obs[0].Produced) != 0 {
		t.Errorf("it claims to have produced something: %+v", obs[0])
	}
}

// §05.3 — the same document unchanged is one row; changed content is a new
// row, so a quiet edit at the source is visible instead of overwriting.
func TestChangedContentAtTheSameURLIsANewLedgerRow(t *testing.T) {
	s := newStore(t)
	same := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting, Org: "A司", Unit: "c业务组", Title: "招后端", Text: "v1", FetchedAt: day},
	}}
	ctx := context.Background()
	if _, err := s.RunDaily(ctx, amy, []leadgraph.Source{same}, leadgraph.FetchRequest{}, day, time.Hour); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if _, err := s.RunDaily(ctx, amy, []leadgraph.Source{same}, leadgraph.FetchRequest{}, day.AddDate(0, 0, 1), time.Hour); err != nil {
		t.Fatalf("day 2: %v", err)
	}
	if n := len(s.Observations(amy, "A司")); n != 1 {
		t.Fatalf("an unchanged document was filed twice: %d rows", n)
	}

	same.docs[0].Text = "v2 - 岗位关闭"
	if _, err := s.RunDaily(ctx, amy, []leadgraph.Source{same}, leadgraph.FetchRequest{}, day.AddDate(0, 0, 2), time.Hour); err != nil {
		t.Fatalf("day 3: %v", err)
	}
	if n := len(s.Observations(amy, "A司")); n != 2 {
		t.Errorf("the source changed under us and the ledger did not notice: %d rows", n)
	}
}

// §05.3 — "how long since this group advertised" comes from the ledger, and
// "never seen" is nil rather than a very large number pretending to be a fact.
func TestHiringGapComesFromTheLedgerAndAdmitsNotKnowing(t *testing.T) {
	s := newStore(t)
	if got := s.HiringGap(amy, "A司", "c业务组", day); got != nil {
		t.Errorf("a group we never observed reported a gap of %v", *got)
	}
	src := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting,
			Org: "A司", Unit: "c业务组", Title: "招后端", PostedAt: at(-30), FetchedAt: day},
	}}
	if _, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour); err != nil {
		t.Fatalf("daily: %v", err)
	}
	gap := s.HiringGap(amy, "A司", "c业务组", day)
	if gap == nil {
		t.Fatal("an observed group reports no gap")
	}
	if h := gap.Hours(); h < 29*24 || h > 31*24 {
		t.Errorf("gap is %v, want about 30 days", *gap)
	}
}

// §05.7 — the alert states two facts the user already gave us: what happened,
// and who they know there. It predicts nothing about anybody's intentions.
func TestAlertNamesThePeopleYouKnowInThatGroup(t *testing.T) {
	s := newStore(t)
	for _, who := range []string{"王五", "张三", "李四"} {
		p := person("A司", who)
		p.UnitPath = []string{"A司", "c业务组"}
		mustNode(t, s, amy, leadgraph.ActorUser, p)
	}
	mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "外人")) // different group

	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
			Org: "A司", Unit: "c业务组", Title: "c业务组并入b业务组", PostedAt: at(-1), FetchedAt: day},
	}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(rep.Alerts) != 1 {
		t.Fatalf("want one alert, got %v", rep.Alerts)
	}
	as := s.Alerts(amy, false)
	if len(as) != 1 {
		t.Fatalf("want one alert on the board, got %d", len(as))
	}
	if len(as[0].People) != 3 {
		t.Fatalf("want the 3 people in c业务组, got %+v", as[0].People)
	}
	if as[0].Corroboration != leadgraph.Announced {
		t.Errorf("an official filing raised a %s alert", as[0].Corroboration)
	}
	// A teammate sees it too: a reorganisation matters to everybody working
	// that company, not only to whoever's daily pass noticed.
	if len(s.Alerts(ben, false)) != 1 {
		t.Error("the alert did not reach the rest of the team")
	}
	if len(s.Alerts(cara, false)) != 0 {
		t.Error("the alert crossed a team boundary")
	}
}

// §05.7 — one event, one alert, ever. A repeated alert is a nudge to act, and
// this feature exists so nothing is missed, not to push anybody into a call.
func TestAnEventAlertsOnlyOnce(t *testing.T) {
	s := newStore(t)
	p := person("A司", "王五")
	p.UnitPath = []string{"A司", "c业务组"}
	mustNode(t, s, amy, leadgraph.ActorUser, p)

	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
			Org: "A司", Unit: "c业务组", Title: "c业务组并入b业务组", PostedAt: at(-1), FetchedAt: day},
	}}
	ctx := context.Background()
	for i := range 3 {
		rep, err := s.RunDaily(ctx, amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day.AddDate(0, 0, i), time.Hour)
		if err != nil {
			t.Fatalf("day %d: %v", i, err)
		}
		if i > 0 && len(rep.Alerts) != 0 {
			t.Errorf("day %d raised the same alert again: %v", i, rep.Alerts)
		}
	}
	if n := len(s.Alerts(amy, true)); n != 1 {
		t.Fatalf("want 1 alert after 3 passes, got %d", n)
	}
}

// §05.3 — the daily pass never asks. Interrupting somebody because a scheduled
// job woke up is how a notification becomes something people turn off.
//
// This is the case where there is nothing to ask about. The case where there
// IS - and where the pass parks it instead of guessing - is
// TestTheDailyPassParksTheDecisionsItRaises.
func TestTheDailyPassAsksNothingAndChangesNoConfirmedFact(t *testing.T) {
	s := newStore(t)
	base := person("A司", "王五")
	base.RoleTitle = "组长"
	existing := mustNode(t, s, amy, leadgraph.ActorUser, base)

	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocAnnouncement,
			Org: "A司", Title: "组织调整公告", PostedAt: at(-1), FetchedAt: day},
	}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	// DailyReport has no Question field at all: the type makes asking impossible.
	if len(rep.Applied) != 1 {
		t.Fatalf("want the announcement recorded, got %+v", rep.Applied)
	}
	if ev, _ := s.Node(amy, rep.Applied[0]); ev.Kind != leadgraph.KindEvent {
		t.Errorf("the pass produced a %s, not an event", ev.Kind)
	}
	got, ok := s.Node(amy, existing.ID)
	if !ok || got.RoleTitle != "组长" {
		t.Errorf("the pass touched a fact nobody confirmed: %+v", got)
	}
	if n := len(s.Pending(amy)); n != 0 {
		t.Errorf("the pass queued %d questions it had no business raising", n)
	}
}

// §07 — a refusal is auditable. A refusal nobody can evidence looks exactly
// like a fetch that never happened.
func TestARefusedFetchIsAudited(t *testing.T) {
	s := newStore(t)
	bad := &fakeSource{name: "maimai", hosts: []string{"maimai.cn"}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{bad}, leadgraph.FetchRequest{}, day, time.Hour)
	if err != nil {
		t.Fatalf("the pass stopped because one source was refused: %v", err)
	}
	if len(rep.Refused) != 1 {
		t.Fatalf("the refusal is not in the report: %+v", rep)
	}
	var found bool
	for _, e := range s.AuditTrail(amy) {
		if e.Action == leadgraph.AuditFetchRefused && e.Detail["host"] == "maimai.cn" {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal is not in the audit trail: %+v", s.AuditTrail(amy))
	}
}

// ---- P9 ----

// §07.2 — sensitive personal information is refused at the write, in every
// path that accepts free text, and the message says what to fix.
func TestSensitiveInformationIsRefusedAtEveryWrite(t *testing.T) {
	s := newStore(t)
	p := person("A司", "王五")
	p.Duty = "负责社招；他老婆刚怀孕所以不想动"
	_, err := s.UpsertNode(amy, leadgraph.ActorUser, p)
	if err == nil || !strings.Contains(err.Error(), "SENSITIVE_FIELD") {
		t.Fatalf("want SENSITIVE_FIELD, got %v", err)
	}
	if !strings.Contains(err.Error(), "怀孕") || !strings.Contains(err.Error(), "health") {
		t.Errorf("the refusal does not say what to fix: %v", err)
	}

	clean := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: clean.ID, At: day, Via: leadgraph.ChannelPhone, Outcome: "他说等离婚办完再看",
	}); err == nil || !strings.Contains(err.Error(), "SENSITIVE_FIELD") {
		t.Errorf("a contact record carried it through: %v", err)
	}
	if err := s.Annotate(amy, leadgraph.ActorUser, clean.ID, 0, "记一下他的身份证号"); err == nil ||
		!strings.Contains(err.Error(), "SENSITIVE_FIELD") {
		t.Errorf("a private note carried it through: %v", err)
	}
	// And an ordinary record still goes in.
	ok := person("A司", "张三")
	ok.Duty = "负责社招初筛"
	if _, err := s.UpsertNode(amy, leadgraph.ActorUser, ok); err != nil {
		t.Errorf("an ordinary duty was refused: %v", err)
	}
}

// §07 — "where did this come from" is answerable for any record.
func TestProvenanceAnswersWhereItCameFrom(t *testing.T) {
	s := newStore(t)
	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
			Org: "A司", Title: "c业务组并入b业务组", Text: "登记变更明细", PostedAt: at(-1), FetchedAt: day},
	}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	got, ok := s.Provenance(amy, rep.Applied[0])
	if !ok {
		t.Fatal("no provenance for a record we just created")
	}
	if len(got.Intel) == 0 || got.Intel[0].SourceURL != "https://reg.example.com/1" {
		t.Errorf("the evidence is missing: %+v", got.Intel)
	}
	if len(got.Observations) != 1 {
		t.Errorf("the ledger row behind it is not linked: %+v", got.Observations)
	}
	if got.Corroboration != leadgraph.Announced {
		t.Errorf("corroboration lost: %s", got.Corroboration)
	}
}

// §07.3 — a subject access request crosses the team boundary, because a
// person's rights are about them, not about our tenancy model.
func TestSubjectAccessCrossesTeams(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	mustNode(t, s, cara, leadgraph.ActorUser, person("A司", "王五"))
	if err := s.Annotate(amy, leadgraph.ActorUser, a.ID, 2, "私人印象"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: a.ID, At: day, Via: leadgraph.ChannelPhone, Topic: "初次沟通",
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}

	recs := s.SubjectRecords(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)
	if len(recs) != 2 {
		t.Fatalf("want both teams' records, got %d", len(recs))
	}
	var withNote int
	for _, r := range recs {
		if len(r.PrivateNotes) > 0 {
			withNote++
		}
	}
	if withNote != 1 {
		t.Errorf("private notes were withheld from the subject: %+v", recs)
	}
	var access int
	for _, e := range s.AuditTrail(amy) {
		if e.Action == leadgraph.AuditSubjectAccess {
			access++
		}
	}
	if access != 1 {
		t.Errorf("the access request was not audited: %d", access)
	}
}

// §07.3 / §11 — deletion is complete and evidenced. Afterwards the person is
// not in either team's export, roster or contact log.
func TestSubjectDeleteIsCompleteAndEvidenced(t *testing.T) {
	s := newStore(t)
	a := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	b := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: a.ID, To: b.ID, Intel: []leadgraph.Intel{said("认识")},
	})
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: a.ID, At: day, Via: leadgraph.ChannelPhone, Topic: "初次沟通",
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}
	if err := s.Annotate(amy, leadgraph.ActorUser, a.ID, 3, "很熟"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	mustNode(t, s, cara, leadgraph.ActorUser, person("A司", "王五"))

	receipts := s.ForgetSubject(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)
	if len(receipts) != 2 {
		t.Fatalf("want a receipt per team, got %+v", receipts)
	}
	var total int
	for _, r := range receipts {
		total += r.Edges + r.Touchpoints + r.Annotations
	}
	if total == 0 {
		t.Errorf("the receipt claims nothing was removed: %+v", receipts)
	}

	for _, v := range []leadgraph.View{amy, ben, cara} {
		bundle, err := s.Export(v, day)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		for _, n := range bundle.Nodes {
			if n.Label == "王五" {
				t.Errorf("%s's export still contains the subject", v.SeatID)
			}
		}
		for _, tp := range bundle.Touchpoints {
			if tp.PersonID == a.ID {
				t.Errorf("%s's export still contains a contact with the subject", v.SeatID)
			}
		}
		for _, e := range bundle.Edges {
			if e.From == a.ID || e.To == a.ID {
				t.Errorf("%s's export still has an edge naming the subject", v.SeatID)
			}
		}
	}
	if len(s.SubjectRecords(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)) != 0 {
		t.Error("the subject can still be found after deletion")
	}
	var deletes int
	for _, e := range s.AuditTrail(amy) {
		if e.Action == leadgraph.AuditSubjectDelete {
			deletes++
		}
	}
	if deletes != 1 {
		t.Errorf("the deletion was not audited: %d", deletes)
	}
}

// §07 — an export is the quietest way for one seat to walk out with everybody
// else's judgements.
func TestExportCarriesOnlyYourOwnNotes(t *testing.T) {
	s := newStore(t)
	n := person("A司", "王五")
	n.Note = "amy 的私人印象"
	created := mustNode(t, s, amy, leadgraph.ActorUser, n)
	if err := s.Annotate(ben, leadgraph.ActorUser, created.ID, 0, "ben 的私人印象"); err != nil {
		t.Fatalf("annotate: %v", err)
	}

	bundle, err := s.Export(ben, day)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, x := range bundle.Nodes {
		if strings.Contains(x.Note, "amy") {
			t.Errorf("ben's export contains amy's private note: %q", x.Note)
		}
		if x.ID == created.ID && x.Note != "ben 的私人印象" {
			t.Errorf("ben's own note is missing from his export: %q", x.Note)
		}
	}
	var exports int
	for _, e := range s.AuditTrail(ben) {
		if e.Action == leadgraph.AuditExport {
			exports++
		}
	}
	if exports != 1 {
		t.Errorf("the export was not audited: %d", exports)
	}
}

// §05.3 — the stale list is ordered and every entry says what to go back to.
func TestStaleScanIsOrderedAndActionable(t *testing.T) {
	s := newStore(t)
	bare := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五")) // unconfirmed fields
	full := person("A司", "张三")
	full.RoleTitle, full.Duty = "组长", "社招初筛"
	done := mustNode(t, s, amy, leadgraph.ActorUser, full)
	due := day.AddDate(0, 0, -1)
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: done.ID, At: day.AddDate(0, 0, -200), Via: leadgraph.ChannelPhone,
		NextStep: "再打一次", DueAt: &due,
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}

	first := s.StaleScan(amy, day, 90*24*time.Hour)
	if len(first) < 3 {
		t.Fatalf("want unconfirmed + cold + overdue, got %+v", first)
	}
	var kinds = map[string]bool{}
	for _, x := range first {
		kinds[x.Kind] = true
		if x.Label == "" {
			t.Errorf("an entry with no name to act on: %+v", x)
		}
	}
	for _, want := range []string{leadgraph.StaleUnconfirmed, leadgraph.StaleColdContact, leadgraph.StaleOverdueTouch} {
		if !kinds[want] {
			t.Errorf("missing %s", want)
		}
	}
	for range 10 {
		next := s.StaleScan(amy, day, 90*24*time.Hour)
		for i := range first {
			if first[i].Kind != next[i].Kind || first[i].NodeID != next[i].NodeID {
				t.Fatalf("stale list order changed at %d", i)
			}
		}
	}
	_ = bare
}

// §05.7 — an event names a company and a unit; the chart holds that unit
// wherever the user placed it, which is usually deeper. Matching the whole path
// found nobody exactly when the user had told us MORE about the structure.
//
// Caught by the first visual walkthrough of a realistic graph: the alert read
// "你图上这个组还没有人" above three people who were in that group.
func TestAnAlertFindsPeopleWhenTheGroupSitsDeeperThanTheEventSaid(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser,
		unit("A司", "c业务组", "A司", "技术中心", "c业务组"))
	for _, who := range []string{"王五", "张三", "李四"} {
		p := person("A司", who)
		p.UnitPath = []string{"A司", "技术中心", "c业务组"} // three levels
		mustNode(t, s, amy, leadgraph.ActorUser, p)
	}

	// The extractor only ever knows company + unit: two levels.
	src := &fakeSource{name: "reg", hosts: []string{"reg.example.com"}, docs: []leadgraph.Document{
		{URL: "https://reg.example.com/1", Kind: leadgraph.DocRegistryChange,
			Org: "A司", Unit: "c业务组", Title: "c业务组并入b业务组", PostedAt: at(-1), FetchedAt: day},
	}}
	if _, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour); err != nil {
		t.Fatalf("daily: %v", err)
	}
	as := s.Alerts(amy, false)
	if len(as) != 1 {
		t.Fatalf("want one alert, got %d", len(as))
	}
	if len(as[0].People) != 3 {
		t.Fatalf("the alert named %d people; the group has 3", len(as[0].People))
	}
}

// §05.3 — path two DOES raise decisions, and it parks them instead of guessing.
//
// This branch had no producer until 2026-09-10. The reason turned out not to be
// "documents cannot contradict the graph" but a silent defect: the user records
// 「c业务组」 under 技术中心, a job advert produces 「c业务组」 directly under the
// company, the natural keys differ, and reconcile skipped same-label candidates
// on the assumption that an identical label always meant an exact match. So a
// second copy of the group appeared with nobody asked - which is what put the
// same group on the lead board twice.
func TestTheDailyPassParksTheDecisionsItRaises(t *testing.T) {
	s := newStore(t)
	// The user's own record, with the structure they know.
	mustNode(t, s, amy, leadgraph.ActorUser, unit("A司", "c业务组", "A司", "技术中心", "c业务组"))

	// The advert knows only company and unit.
	src := &fakeSource{name: "board", hosts: []string{"careers.example.com"}, docs: []leadgraph.Document{
		{URL: "https://careers.example.com/jd/1", Kind: leadgraph.DocJobPosting,
			Org: "A司", Unit: "c业务组", Title: "招后端", FetchedAt: day},
	}}
	rep, err := s.RunDaily(context.Background(), amy, []leadgraph.Source{src}, leadgraph.FetchRequest{}, day, time.Hour)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}

	if len(rep.Queued) != 1 {
		t.Fatalf("want the decision parked, got queued=%v applied=%v", rep.Queued, rep.Applied)
	}
	if len(rep.Applied) != 0 {
		t.Errorf("a second copy of the group was created instead of asking: %v", rep.Applied)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindUnit})); n != 1 {
		t.Errorf("the graph now holds %d copies of one group", n)
	}

	pending := s.Pending(amy)
	if len(pending) != 1 || pending[0].Proposal.Decision != leadgraph.DecideMerge {
		t.Fatalf("want one merge decision waiting, got %+v", pending)
	}
	if len(pending[0].Proposal.Merges) == 0 ||
		pending[0].Proposal.Merges[0].Reasons[0] != leadgraph.RuleSameOrgSameLabelOtherPath {
		t.Errorf("the question does not say why it was asked: %+v", pending[0].Proposal.Merges)
	}

	// And the person who owns the queue can answer it, which is the whole point
	// of parking it rather than guessing.
	if _, err := s.Answer(amy, leadgraph.ActorUser, pending[0].ID, leadgraph.Resolution{
		Choice: leadgraph.ChooseMergeInto, MergeIntoID: pending[0].Proposal.Merges[0].NodeID,
	}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindUnit})); n != 1 {
		t.Errorf("answering produced %d groups", n)
	}
	if len(s.Pending(amy)) != 0 {
		t.Error("the answered question is still queued")
	}
}

// The event a recruiter TELLS the agent about must raise an alert.
//
// This is the defect 离职提醒 was reported as: the pass only ever raised alerts
// for events its own fetch had just created, and this deployment has no sources
// configured, so nothing ever raised anything. The event that matters — the one
// the user described when they asked for this feature, "A司c组并入b组" — arrives
// through record_turn and was invisible to it.
func TestTheDailyPassAlertsOnEventsTheConversationRecorded(t *testing.T) {
	s := newStore(t)
	ev := mustNode(t, s, amy, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindEvent, Org: "A司", Label: "c业务组并入b业务组",
		UnitPath: []string{"A司", "c业务组"},
		Intel:    []leadgraph.Intel{said("c组要并进b组")},
	})
	if got := s.Alerts(amy, true); len(got) != 0 {
		t.Fatalf("an alert existed before any pass ran: %d", len(got))
	}

	rep, err := s.RunDaily(context.Background(), amy, nil, leadgraph.FetchRequest{},
		time.Now().UTC(), 45*24*time.Hour)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(rep.Alerts) != 1 {
		t.Fatalf("the pass raised %d alerts for an event it did not fetch itself", len(rep.Alerts))
	}
	got := s.Alerts(amy, false)
	if len(got) != 1 || got[0].EventID != ev.ID {
		t.Fatalf("the alert does not name the event: %+v", got)
	}
}

// And running it again changes nothing. The pass is a reconciliation, so it has
// to be safe to run on every schedule tick, on every restart, forever.
func TestTheDailyPassIsIdempotent(t *testing.T) {
	s := newStore(t)
	mustNode(t, s, amy, leadgraph.ActorUser, leadgraph.Node{
		Kind: leadgraph.KindEvent, Org: "A司", Label: "c业务组并入b业务组",
		UnitPath: []string{"A司", "c业务组"},
		Intel:    []leadgraph.Intel{said("c组要并进b组")},
	})
	now := time.Now().UTC()
	for i := range 3 {
		rep, err := s.RunDaily(context.Background(), amy, nil, leadgraph.FetchRequest{}, now, 45*24*time.Hour)
		if err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		if want := 1; i > 0 && len(rep.Alerts) != 0 {
			t.Errorf("pass %d raised %d alerts again (want %d only on the first)", i, len(rep.Alerts), want-1)
		}
	}
	if got := s.Alerts(amy, true); len(got) != 1 {
		t.Errorf("three passes left %d alerts for one event", len(got))
	}
}
