package leadgraph_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

var updateImportGolden = flag.Bool("update-import-golden", false, "rewrite testdata/import/csv-plans.golden.json")

// TestASpreadsheetIsPlannedExactlyAsBefore pins what a CSV import plans, byte for
// byte, across the shapes a real file takes.
//
// Why it exists: the screenshot import plans through the same per-row code a CSV
// does, and moving that code out of PlanImport to share it is the kind of change
// that alters a skipped-row reason or a line number without any other test
// noticing. The golden file was written from the code as it stood BEFORE that
// move (2026-09-11), so any difference is a change to the spreadsheet import,
// not to the screenshot one.
func TestASpreadsheetIsPlannedExactlyAsBefore(t *testing.T) {
	cases := []struct {
		name string
		file string
		raw  []byte
		ov   leadgraph.ImportOverrides
	}{
		{name: "plain", file: "contacts.csv", raw: []byte(plainCSV)},
		{name: "gbk", file: "从Excel导出.csv", raw: gbk(t, plainCSV)},
		{name: "tabs and bom", file: "crm.tsv",
			raw: []byte("\ufeffName\tCompany\tTeam\tTitle\tPhone\tEmail\n王五\tA司\tc业务组\t组长\t13800000000\twang@example.com\n\t\t\t\t\t\n")},
		{name: "skipped and sensitive", file: "messy.csv",
			raw: []byte("姓名,公司,职位,熟悉程度,备注\n,A司,组长,3,\n张三,A司,工程师,很熟,\n李四,B司,经理,2,他在做化疗\n")},
		{name: "corrected", file: "messy.csv",
			raw: []byte("姓名,公司,职位,熟悉程度,备注\n,A司,组长,3,\n张三,A司,工程师,很熟,\n李四,B司,经理,2,\n"),
			ov: leadgraph.ImportOverrides{
				Values: map[leadgraph.Column]map[string]string{leadgraph.ColStrength: {"很熟": "3"}},
				Rows:   map[int]map[leadgraph.Column]string{2: {leadgraph.ColLabel: "王五"}},
				Ignore: []int{4},
			}},
		{name: "no name column", file: "odd.csv", raw: []byte("甲,乙\n王五,A司\n")},
	}
	got := map[string]leadgraph.ImportPlan{}
	for _, c := range cases {
		plan, err := newStore(t).PlanImport(amy, c.file, c.raw, c.ov)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got[c.name] = plan
	}
	body, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "import", "csv-plans.golden.json")
	if *updateImportGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file: %v (write it with -update-import-golden from code that has not changed)", err)
	}
	if string(want) != string(body) {
		t.Fatalf("a spreadsheet import now plans differently from before. If that was intended, rewrite the "+
			"golden file with -update-import-golden and say why in the commit.\n--- got ---\n%s", body)
	}
}
