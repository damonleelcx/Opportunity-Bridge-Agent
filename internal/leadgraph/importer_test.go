package leadgraph_test

// Fences over bulk import.
//
// The claim: we adapt to the user's file rather than making them adapt to us,
// we never write without showing the plan first, and nothing a row carries can
// bypass a rule the conversation path obeys.

import (
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func gbk(t *testing.T, s string) []byte {
	t.Helper()
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(s))
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	return out
}

const plainCSV = "姓名,公司,部门,职位,熟悉程度,备注\n" +
	"王五,A司,c业务组,组长,3,老朋友\n" +
	"张三,A司,c业务组,高级工程师,2,\n" +
	"b2,C司,平台组,平台负责人,,\n"

// The file arrives as the user has it: whatever their CRM called the columns.
func TestHeadersAreMatchedNotDictated(t *testing.T) {
	s := newStore(t)
	// Every header here is a different word for the same thing.
	file := "Full Name,雇主,Team,Position,closeness\n" +
		"王五,A司,c业务组,组长,3\n"
	plan, err := s.PlanImport(amy, "crm.csv", []byte(file))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for field, want := range map[string]string{
		"label": "Full Name", "org": "雇主", "unit": "Team", "role_title": "Position", "strength": "closeness",
	} {
		if plan.Mapping[field] != want {
			t.Errorf("%s mapped to %q, want %q", field, plan.Mapping[field], want)
		}
	}
	if len(plan.Proposals) != 1 || plan.Proposals[0].Candidate.RoleTitle != "组长" {
		t.Fatalf("the row did not read correctly: %+v", plan.Proposals)
	}
}

// A CSV exported from a Chinese Excel is GBK. Getting this wrong produces an
// import that "succeeds" and fills the graph with 乱码.
func TestAGBKFileIsReadCorrectly(t *testing.T) {
	s := newStore(t)
	plan, err := s.PlanImport(amy, "从Excel导出.csv", gbk(t, plainCSV))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Encoding != "gbk" {
		t.Errorf("encoding detected as %q", plan.Encoding)
	}
	if len(plan.Proposals) != 3 {
		t.Fatalf("want 3 rows, got %d", len(plan.Proposals))
	}
	if plan.Proposals[0].Candidate.Label != "王五" {
		t.Errorf("garbled: %q", plan.Proposals[0].Candidate.Label)
	}
}

// A UTF-8 BOM and a tab-separated file both arrive from real exports.
func TestBOMAndTabsAreHandled(t *testing.T) {
	s := newStore(t)
	tsv := "\xEF\xBB\xBF姓名\t公司\t职位\n王五\tA司\t组长\n"
	plan, err := s.PlanImport(amy, "export.tsv", []byte(tsv))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Encoding != "utf-8-bom" || plan.Delimiter != "\t" {
		t.Errorf("encoding=%q delimiter=%q", plan.Encoding, plan.Delimiter)
	}
	if len(plan.Proposals) != 1 || plan.Proposals[0].Candidate.Org != "A司" {
		t.Fatalf("the row did not read correctly: %+v", plan.Proposals)
	}
}

// Without a name column there is nothing to import, and the refusal has to say
// what it DID see - otherwise the person holding the file has no next move.
func TestAFileWithNoNameColumnIsRefusedWithWhatWeSaw(t *testing.T) {
	s := newStore(t)
	_, err := s.PlanImport(amy, "wrong.csv", []byte("公司,部门,电话\nA司,c业务组,138\n"))
	if err == nil {
		t.Fatal("a file with no name column was accepted")
	}
	if !strings.Contains(err.Error(), "公司") || !strings.Contains(err.Error(), "电话") {
		t.Errorf("the refusal does not list the columns it saw: %v", err)
	}
}

// A column we do not use is REPORTED. A column of phone numbers that quietly
// vanishes is the difference between an import and a partial import nobody
// was told about.
func TestUnusedColumnsAreReported(t *testing.T) {
	s := newStore(t)
	plan, err := s.PlanImport(amy, "x.csv", []byte("姓名,手机,邮箱\n王五,13800000000,a@b.com\n"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Unmapped) != 2 {
		t.Fatalf("want both unused columns reported, got %v", plan.Unmapped)
	}
	joined := strings.Join(plan.Unmapped, ",")
	if !strings.Contains(joined, "手机") || !strings.Contains(joined, "邮箱") {
		t.Errorf("unmapped columns are wrong: %v", plan.Unmapped)
	}
}

// Planning writes nothing. An import the user has not looked at yet has not
// happened.
func TestPlanningWritesNothing(t *testing.T) {
	s := newStore(t)
	if _, err := s.PlanImport(amy, "x.csv", []byte(plainCSV)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("planning wrote %d records", n)
	}
	if n := len(s.AuditTrail(amy)); n != 0 {
		t.Errorf("planning recorded %d audit entries", n)
	}
}

// The whole point: after an import, path_find has somewhere to start.
func TestImportSeedsTheRelationshipStrengths(t *testing.T) {
	s := newStore(t)
	plan, err := s.PlanImport(amy, "contacts.csv", []byte(plainCSV))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ApplyImport(amy, plan, nil, day)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Created) != 3 {
		t.Fatalf("want 3 people created, got %+v", res)
	}
	if res.Rated != 2 {
		t.Errorf("want the two rated rows applied, got %d", res.Rated)
	}
	if len(s.Seeds(amy)) != 2 {
		t.Fatalf("path_find still has nowhere to start: %d seeds", len(s.Seeds(amy)))
	}
	// Strongest first, so the best routes surface first.
	if s.Seeds(amy)[0].Label != "王五" {
		t.Errorf("seeds are not strongest-first: %+v", s.Seeds(amy))
	}
}

// An import goes through Reconcile like everything else: it cannot create the
// duplicate the conversation path would have asked about.
func TestImportCannotCreateADuplicateTheConversationWouldHaveQueried(t *testing.T) {
	s := newStore(t)
	existing := mustNode(t, s, amy, leadgraph.ActorUser, inUnit(person("A司", "王五"), "A司", "c业务组"))

	// The file calls him 王总 - the same address-form case Reconcile handles.
	plan, err := s.PlanImport(amy, "x.csv", []byte("姓名,公司,部门\n王总,A司,c业务组\n"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Proposals) != 1 || plan.Proposals[0].Decision != leadgraph.DecideMerge {
		t.Fatalf("want a merge proposal, got %+v", plan.Proposals)
	}
	if c := plan.Counts(); c["needs_you"] != 1 || c["create"] != 0 {
		t.Errorf("the plan does not tell the user a decision is needed: %+v", c)
	}

	res, err := s.ApplyImport(amy, plan, nil, day)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Pending) != 1 {
		t.Errorf("the undecided row was applied: %+v", res)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 1 {
		t.Errorf("import created a duplicate: %d people", n)
	}

	// And with the user's answer it folds in rather than duplicating.
	if _, err := s.ApplyImport(amy, plan, map[int]leadgraph.Resolution{
		0: {Choice: leadgraph.ChooseMergeInto, MergeIntoID: existing.ID},
	}, day); err != nil {
		t.Fatalf("apply with answer: %v", err)
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 1 {
		t.Errorf("answering produced %d people", n)
	}
}

// §07 — one sensitive cell loses that FIELD, not the row and certainly not the
// file. Refusing the whole import would have the user delete a column blindly
// and try again.
func TestASensitiveCellLosesTheFieldNotTheFile(t *testing.T) {
	s := newStore(t)
	file := "姓名,公司,备注\n" +
		"王五,A司,他老婆刚怀孕不想动\n" +
		"张三,A司,想去做平台\n"
	plan, err := s.PlanImport(amy, "x.csv", []byte(file))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Proposals) != 2 {
		t.Fatalf("a sensitive cell cost us %d of 2 rows", len(plan.Proposals))
	}
	if _, kept := plan.Notes[0]; kept {
		t.Errorf("the sensitive note survived: %q", plan.Notes[0])
	}
	if plan.Notes[1] != "想去做平台" {
		t.Errorf("an innocent note was dropped: %q", plan.Notes[1])
	}
	var told bool
	for _, sk := range plan.Skipped {
		if sk.Reason == leadgraph.SkipSensitive && sk.Row == 2 && strings.Contains(sk.Detail, "怀孕") {
			told = true
		}
	}
	if !told {
		t.Errorf("the user is not told which cell was dropped: %+v", plan.Skipped)
	}
	res, err := s.ApplyImport(amy, plan, nil, day)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Noted != 1 {
		t.Fatalf("want the one innocent note applied, got %d", res.Noted)
	}
	// The note lands in the importer's own overlay and nowhere else.
	for _, n := range s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}) {
		if n.Label == "张三" && n.Note != "想去做平台" {
			t.Errorf("the note did not reach its author: %q", n.Note)
		}
		if n.Label == "王五" && n.Note != "" {
			t.Errorf("the sensitive note landed anyway: %q", n.Note)
		}
	}
	for _, n := range s.Nodes(ben, leadgraph.NodeFilter{Kind: leadgraph.KindPerson}) {
		if n.Note != "" {
			t.Errorf("an imported private note reached a teammate: %q", n.Note)
		}
	}
}

// Rows that cannot become a record are reported with their line number.
func TestUnusableRowsAreReportedWithLineNumbers(t *testing.T) {
	s := newStore(t)
	file := "姓名,公司,熟悉程度\n" +
		"王五,A司,3\n" +
		",A司,2\n" + // no name
		"张三,A司,很熟\n" // not 1-3
	plan, err := s.PlanImport(amy, "x.csv", []byte(file))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	byRow := map[int]leadgraph.SkippedRow{}
	for _, sk := range plan.Skipped {
		byRow[sk.Row] = sk
	}
	if byRow[3].Reason != leadgraph.SkipNoName {
		t.Errorf("the nameless row is not reported: %+v", plan.Skipped)
	}
	if byRow[4].Reason != leadgraph.SkipBadValue || !strings.Contains(byRow[4].Detail, "很熟") {
		t.Errorf("the unreadable strength is not reported: %+v", plan.Skipped)
	}
	// 张三 still becomes a record; only his strength was unusable.
	if len(plan.Proposals) != 2 {
		t.Errorf("want 2 usable rows, got %d", len(plan.Proposals))
	}
}

// One file saying the same thing twice is ONE source. Counting rows as
// independent would promote a single spreadsheet to corroborated on its own.
func TestOneFileIsOneSourceHoweverManyRows(t *testing.T) {
	s := newStore(t)
	file := "姓名,公司,职位\n王五,A司,组长\n王五,A司,组长\n"
	plan, err := s.PlanImport(amy, "x.csv", []byte(file))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.ApplyImport(amy, plan, nil, day); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ns := s.Nodes(amy, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})
	if len(ns) != 1 {
		t.Fatalf("want one person, got %d", len(ns))
	}
	if got := ns[0].Corroboration(); got != leadgraph.Hearsay {
		t.Errorf("one spreadsheet corroborated itself: %s", got)
	}
}

// §07 — an import is in the ledger and in the audit trail, like every other way
// data gets in.
func TestAnImportIsTraceableAfterwards(t *testing.T) {
	s := newStore(t)
	plan, err := s.PlanImport(amy, "contacts.csv", []byte(plainCSV))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ApplyImport(amy, plan, nil, day)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ObservationID == "" {
		t.Fatal("the import left no ledger row")
	}
	prov, ok := s.Provenance(amy, res.Created[0])
	if !ok {
		t.Fatal("no provenance for an imported record")
	}
	if len(prov.Intel) == 0 || prov.Intel[0].Kind != leadgraph.IntelImported {
		t.Errorf("the record does not say it came from a file: %+v", prov.Intel)
	}
	if !strings.Contains(prov.Intel[0].Excerpt, "contacts.csv") ||
		!strings.Contains(prov.Intel[0].Excerpt, "行") {
		t.Errorf("the evidence does not name the file and row: %q", prov.Intel[0].Excerpt)
	}
	if len(prov.Observations) == 0 {
		t.Error("the ledger row is not linked to the record")
	}
	var audited bool
	for _, e := range s.AuditTrail(amy) {
		if e.Action == leadgraph.AuditImport && e.Detail["file"] == "contacts.csv" {
			audited = true
			if e.Detail["created"] != "3" {
				t.Errorf("the audit entry undercounts: %+v", e.Detail)
			}
		}
	}
	if !audited {
		t.Error("the import is not in the audit trail")
	}
}

// A fetch may not wear the import kind: that provenance means "the user brought
// this file", and a fetched document did not come from the user.
func TestAFetchCannotClaimToBeAnImport(t *testing.T) {
	bad := &fakeSource{name: "x", hosts: []string{"ok.example.com"}, docs: []leadgraph.Document{
		{URL: "https://ok.example.com/1", Kind: leadgraph.DocImportedFile, Org: "A司", Title: "假装是导入"},
	}}
	docs, refused, err := leadgraph.FetchFrom(t.Context(), bad, leadgraph.FetchRequest{}, day)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("a fetch claimed import provenance: %+v", docs)
	}
	if len(refused) != 1 {
		t.Errorf("the refusal was not recorded: %+v", refused)
	}
}

// Import obeys the team boundary like every other write.
func TestImportStaysInsideTheTeam(t *testing.T) {
	s := newStore(t)
	plan, err := s.PlanImport(amy, "x.csv", []byte(plainCSV))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.ApplyImport(amy, plan, nil, day); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := len(s.Nodes(cara, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("another team can see %d imported records", n)
	}
	if n := len(s.Nodes(ben, leadgraph.NodeFilter{Kind: leadgraph.KindPerson})); n != 3 {
		t.Errorf("a teammate cannot see the imported facts: %d", n)
	}
	// The strengths, though, are amy's alone.
	if len(s.Seeds(ben)) != 0 {
		t.Errorf("a teammate inherited amy's relationship strengths: %+v", s.Seeds(ben))
	}
}

var _ = time.Now
