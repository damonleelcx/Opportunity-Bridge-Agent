package leadgraph_test

// Fences over repairing a bad import.
//
// The claim: a file we cannot read is a conversation, not a refusal. The agent
// can see enough to propose a reading, the user confirms it, and the whole file
// is read again - and nothing along that path lets the agent invent a fact.

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

// The file whose headers we do not know: 客户名称 is a real thing a CRM calls
// this column, and 很熟 is how somebody actually writes a rating.
const strangeCSV = "客户名称,所属机构,组别,岗位,关系\n" +
	"王五,A司,c业务组,组长,很熟\n" +
	"张三,A司,c业务组,工程师,一般\n" +
	",A司,c业务组,,很熟\n" // line 4: no name

// An unreadable file is staged with everything somebody needs to work out how
// to read it - the header and a few of its own rows.
func TestAnUnreadableFileIsStagedForInspection(t *testing.T) {
	s := newStore(t)
	sess, err := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	if err != nil {
		t.Fatalf("the file was refused instead of staged: %v", err)
	}
	sum, ok := s.ImportSummary(amy, sess.ID)
	if !ok {
		t.Fatal("the staged file cannot be described")
	}
	if sum.Blocked != leadgraph.BlockNoNameColumn {
		t.Fatalf("the summary does not say why it is stuck: %q", sum.Blocked)
	}
	if len(sum.Header) != 5 {
		t.Fatalf("the header is not available to look at: %v", sum.Header)
	}
	if len(sum.Samples) == 0 || sum.Samples[0][0] != "王五" {
		t.Fatalf("the sample rows are not the file's own: %v", sum.Samples)
	}
	if sum.Counts["create"] != 0 {
		t.Errorf("a blocked plan claims it would create records: %+v", sum.Counts)
	}
}

// A blocked plan cannot be committed by mistake.
func TestABlockedPlanCannotBeCommitted(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	if _, err := s.CommitImport(amy, sess.ID, nil, day); err == nil {
		t.Fatal("a file nobody could read was imported anyway")
	}
	if n := len(s.Nodes(amy, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("the blocked commit wrote %d records", n)
	}
}

// The repair: the user says which column is which, and the WHOLE file is read
// again from the original bytes.
func TestSayingWhichColumnIsWhichUnblocksTheFile(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))

	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns": []any{
			map[string]any{"field": "label", "header": "客户名称"},
			map[string]any{"field": "org", "header": "所属机构"},
			map[string]any{"field": "unit", "header": "组别"},
			map[string]any{"field": "role_title", "header": "岗位"},
			map[string]any{"field": "strength", "header": "关系"},
		},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	sum, _ := got.(leadgraph.ImportSummary)
	if sum.Blocked != "" {
		t.Fatalf("still blocked after the correction: %q", sum.Blocked)
	}
	if sum.Counts["create"] != 2 {
		t.Fatalf("want the two usable rows, got %+v", sum.Counts)
	}
	if sum.Mapping["label"] != "客户名称" {
		t.Errorf("the correction did not stick: %+v", sum.Mapping)
	}
	// The nameless row is still reported, by line number.
	if rows := sum.SkippedRows[leadgraph.SkipNoName]; len(rows) != 1 || rows[0] != 4 {
		t.Errorf("the nameless row is not reported: %+v", sum.SkippedRows)
	}
	// 关系 says 很熟, which is not 1-3, so the ratings are still unusable.
	if sum.RowsWithoutStrength != 2 {
		t.Errorf("want both ratings still unusable, got %d", sum.RowsWithoutStrength)
	}
}

// And the file's own vocabulary is translated rather than treated as a defect.
func TestTheFilesOwnVocabularyCanBeTranslated(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	base := []any{
		map[string]any{"field": "label", "header": "客户名称"},
		map[string]any{"field": "org", "header": "所属机构"},
		map[string]any{"field": "strength", "header": "关系"},
	}
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID, "columns": base,
		"values": []any{
			map[string]any{"field": "strength", "from": "很熟", "to": "3"},
			map[string]any{"field": "strength", "from": "一般", "to": "2"},
		},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	if sum, _ := got.(leadgraph.ImportSummary); sum.RowsWithoutStrength != 0 {
		t.Fatalf("the translation did not take: %d rows still unrated", sum.RowsWithoutStrength)
	}

	args := map[string]any{"import_id": sess.ID}
	if _, err := call(t, s, amy, "import_commit", args, leadgraph.ApprovalFor("import_commit", args)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seeds := s.Seeds(amy)
	if len(seeds) != 2 {
		t.Fatalf("want both people usable as路径起点, got %d", len(seeds))
	}
	// Strongest first: 王五 was 很熟 -> 3.
	if seeds[0].Label != "王五" {
		t.Errorf("the translated strengths are wrong: %+v", seeds)
	}
}

// A value the USER supplied fills a cell the file was missing. The agent may
// relay it; it may not invent it - and nothing here can tell the difference,
// which is why the tool's description says so and the transcript is the record.
func TestAUserSuppliedCellFillsAMissingOne(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	cols := []any{
		map[string]any{"field": "label", "header": "客户名称"},
		map[string]any{"field": "org", "header": "所属机构"},
	}
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID, "columns": cols,
		"rows": []any{map[string]any{"row": 4, "field": "label", "value": "李四"}},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	sum, _ := got.(leadgraph.ImportSummary)
	if sum.Counts["create"] != 3 {
		t.Fatalf("the repaired row did not join the plan: %+v", sum.Counts)
	}
	if len(sum.SkippedRows[leadgraph.SkipNoName]) != 0 {
		t.Errorf("the row is still reported as nameless: %+v", sum.SkippedRows)
	}
}

// And a row the user said to leave out is left out.
func TestARowTheUserRejectedIsLeftOut(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns": []any{
			map[string]any{"field": "label", "header": "客户名称"},
			map[string]any{"field": "org", "header": "所属机构"},
		},
		"ignore": []any{3},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	sum, _ := got.(leadgraph.ImportSummary)
	if sum.Counts["create"] != 1 {
		t.Fatalf("want only 王五, got %+v", sum.Counts)
	}
	if len(sum.SkippedRows[leadgraph.SkipNoName]) != 1 {
		t.Errorf("ignoring a row should not hide the nameless one: %+v", sum.SkippedRows)
	}
}

// A correction is a RE-READ, not a patch: correcting twice does not accumulate
// a half-fixed plan, and the second correction replaces the first.
func TestCorrectingTwiceRereadsRatherThanAccumulates(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))

	// First attempt points at the wrong column for the name.
	if _, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns":   []any{map[string]any{"field": "label", "header": "岗位"}},
	}); err != nil {
		t.Fatalf("first remap: %v", err)
	}
	sum, _ := s.ImportSummary(amy, sess.ID)
	if sum.Mapping["label"] != "岗位" {
		t.Fatalf("the first correction did not apply: %+v", sum.Mapping)
	}

	// The second replaces it wholesale.
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns": []any{
			map[string]any{"field": "label", "header": "客户名称"},
			map[string]any{"field": "org", "header": "所属机构"},
		},
	})
	if err != nil {
		t.Fatalf("second remap: %v", err)
	}
	sum2, _ := got.(leadgraph.ImportSummary)
	if sum2.Mapping["label"] != "客户名称" {
		t.Errorf("the second correction did not replace the first: %+v", sum2.Mapping)
	}
	if sum2.Counts["create"] != 2 {
		t.Errorf("the re-read is not one coherent plan: %+v", sum2.Counts)
	}

	// The proof that corrections do not accumulate: sending none puts the file
	// back exactly where it started. (岗位 and 关系 stay mapped throughout - the
	// alias table knows those words; they were never corrections.)
	got3, err := call(t, s, amy, "import_remap", map[string]any{"import_id": sess.ID})
	if err != nil {
		t.Fatalf("third remap: %v", err)
	}
	if sum3, _ := got3.(leadgraph.ImportSummary); sum3.Blocked != leadgraph.BlockNoNameColumn {
		t.Errorf("an earlier correction survived a re-read that sent none: %q", sum3.Blocked)
	}
}

// Pointing at a column by position, for a file whose header is blank or
// duplicated.
func TestAColumnCanBeNamedByPosition(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "noheader.csv", []byte(",,\n王五,A司,组长\n"))
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns": []any{
			map[string]any{"field": "label", "header": "#1"},
			map[string]any{"field": "org", "header": "#2"},
		},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	sum, _ := got.(leadgraph.ImportSummary)
	if sum.Blocked != "" || sum.Counts["create"] != 1 {
		t.Fatalf("a positional correction did not work: blocked=%q counts=%+v", sum.Blocked, sum.Counts)
	}
}

// A correction that names a column nothing matches is ignored rather than
// silently reading column zero.
func TestANonsenseColumnNameIsIgnoredNotGuessed(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns":   []any{map[string]any{"field": "label", "header": "根本没有这一列"}},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	if sum, _ := got.(leadgraph.ImportSummary); sum.Blocked != leadgraph.BlockNoNameColumn {
		t.Errorf("a nonsense column name mapped to something: %q", sum.Blocked)
	}
}

// Repair obeys the seat boundary: a teammate cannot rewrite how somebody
// else's upload is read.
func TestOnlyTheUploaderCanCorrectAnImport(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	if _, err := s.RestageImport(ben, sess.ID, leadgraph.ImportOverrides{
		Columns: map[leadgraph.Column]string{leadgraph.ColLabel: "客户名称"},
	}); err == nil {
		t.Error("a teammate rewrote somebody else's staged reading")
	}
	if sum, _ := s.ImportSummary(amy, sess.ID); sum.Blocked == "" {
		t.Error("the refused correction applied anyway")
	}
}

// Corrections still cannot get sensitive information in.
func TestARepairCannotSmuggleSensitiveInformationIn(t *testing.T) {
	s := newStore(t)
	sess, _ := s.StageImport(amy, "crm导出.csv", []byte(strangeCSV))
	got, err := call(t, s, amy, "import_remap", map[string]any{
		"import_id": sess.ID,
		"columns": []any{
			map[string]any{"field": "label", "header": "客户名称"},
			map[string]any{"field": "org", "header": "所属机构"},
		},
		"rows": []any{map[string]any{"row": 2, "field": "duty", "value": "他老婆刚怀孕不想动"}},
	})
	if err != nil {
		t.Fatalf("import_remap: %v", err)
	}
	sum, _ := got.(leadgraph.ImportSummary)
	var told bool
	for reason := range sum.SkippedRows {
		if reason == leadgraph.SkipSensitive {
			told = true
		}
	}
	if !told {
		t.Errorf("a sensitive value came in through a repair: %+v", sum.SkippedRows)
	}
}

// A file too large to hold is refused with its size, not accepted and then
// forgotten.
func TestAnOversizedFileIsRefusedWithItsSize(t *testing.T) {
	s := newStore(t)
	big := make([]byte, leadgraph.MaxImportBytes+1)
	_, err := s.StageImport(amy, "huge.csv", big)
	if err == nil {
		t.Fatal("an oversized file was staged")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the refusal does not say what the limit is: %v", err)
	}
}
