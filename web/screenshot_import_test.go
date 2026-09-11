package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Fences over the screenshot import in the interface. The server enforces the
// confirmation (internal/httpapi/screenshot.go); these hold what the page does
// before the server is ever asked. See docs/20-lead-graph.zh-CN.md §10.3.

// funcBody returns one function's source, comments stripped.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		start = strings.Index(src, "async function "+name+"(")
	}
	if start < 0 {
		t.Fatalf("%s is gone; the fence is watching the wrong function", name)
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

func TestTheImportControlTakesAScreenshot(t *testing.T) {
	html := asset(t, "app.html")
	i := strings.Index(html, `id="importFile"`)
	if i < 0 {
		t.Fatal("the import file input is gone")
	}
	tag := html[i : i+strings.Index(html[i:], ">")]
	for _, want := range []string{"image/png", "image/jpeg", "image/webp", ".csv"} {
		if !strings.Contains(tag, want) {
			t.Errorf("the import control does not accept %s: %s", want, tag)
		}
	}
}

// A picture of other people is not posted until the person confirms it, and the
// confirmation is what the request carries - not a flag set for every upload.
func TestAScreenshotIsNotPostedBeforeThePersonConfirms(t *testing.T) {
	src := stripLineComments(asset(t, "app.js"))

	wire := funcBody(t, src, "wireImport")
	img := strings.Index(wire, `file.type.startsWith("image/")`)
	if img < 0 {
		t.Fatal("wireImport no longer tells a picture from a file")
	}
	branch := wire[img:]
	if end := strings.Index(branch, "}"); end > 0 {
		branch = branch[:end]
	}
	if !strings.Contains(branch, "confirmScreenshot(file)") || strings.Contains(branch, "stageImport(") {
		t.Errorf("a picture must go to confirmScreenshot and not straight to stageImport: %q", branch)
	}

	// The only upload that carries the confirmation is the one behind the yes.
	if n := strings.Count(src, "stageImport(file, true)"); n != 1 {
		t.Fatalf("stageImport(file, true) appears %d times, want exactly the one behind the confirmation", n)
	}
	confirm := funcBody(t, src, "confirmScreenshot")
	if !strings.Contains(confirm, "if (confirmed) await stageImport(file, true)") {
		t.Error("the confirmed upload is not guarded by the person's answer")
	}

	stage := funcBody(t, src, "stageImport")
	if !regexp.MustCompile(`if \(confirmed\) body\.append\("vendor_consent", "screenshot_recognition"\)`).MatchString(stage) {
		t.Error("the upload does not carry the confirmation only when it was given")
	}
}

// Where the picture goes is read from the deployment, never written in the page.
// A privacy statement in copy was false in production once already.
func TestTheConfirmationNamesTheDeploymentsVendor(t *testing.T) {
	src := stripLineComments(asset(t, "app.js"))
	confirm := funcBody(t, src, "confirmScreenshot")
	for _, want := range []string{"vision_endpoint_host", "vision_model", `"import.shotConfirmUnknown"`, "screenshot_import_enabled === false"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirmScreenshot does not use %s", want)
		}
	}
	for _, vendor := range []string{"阿里", "通义", "dashscope", "aliyuncs", "qwen"} {
		if strings.Contains(strings.ToLower(confirm), vendor) {
			t.Errorf("confirmScreenshot names a vendor itself (%q); it must come from /api/health", vendor)
		}
	}
	i18n := asset(t, "i18n.js")
	if strings.Count(i18n, `"import.shotConfirmBody"`) != 2 {
		t.Fatal("the confirmation body is not in both languages")
	}
	for _, line := range strings.Split(i18n, "\n") {
		if strings.Contains(line, `"import.shotConfirmBody"`) && (!strings.Contains(line, "{host}") || !strings.Contains(line, "{model}")) {
			t.Errorf("a confirmation body does not name the host and model it is sent to: %s", strings.TrimSpace(line))
		}
	}
}

// Every reason a person is held and every check a row can raise has a sentence,
// in both languages. Read from the source, so a new one turns this red.
func TestEveryHoldAndCheckHasAWord(t *testing.T) {
	src, err := os.ReadFile("../internal/leadgraph/screenshot_plan.go")
	if err != nil {
		t.Fatal(err)
	}
	table := stringsTable(t)
	for prefix, decl := range map[string]*regexp.Regexp{
		"hold.":  regexp.MustCompile(`Hold[A-Za-z]+\s+=\s+"([a-z_]+)"`),
		"check.": regexp.MustCompile(`Check[A-Za-z]+\s+=\s+"([a-z_]+)"`),
	} {
		keys := decl.FindAllStringSubmatch(string(src), -1)
		if len(keys) == 0 {
			t.Fatalf("found no %s keys; the fence is not reading the source", prefix)
		}
		for _, m := range keys {
			for lang, set := range table {
				if !set[prefix+m[1]] {
					t.Errorf("a screenshot row can carry %q and %s has no sentence for it", m[1], lang)
				}
			}
		}
	}
}

// Every person a screenshot was read as is shown beside the words they were
// read from. A wrongly read name looks exactly like a rightly read one otherwise.
func TestAScreenshotPlanShowsEveryPersonBesideItsText(t *testing.T) {
	src := stripLineComments(asset(t, "app.js"))
	if !strings.Contains(funcBody(t, src, "importPlanCard"), "screenshotRows(r)") {
		t.Fatal("the import plan card does not render a screenshot's rows")
	}
	rows := funcBody(t, src, "screenshotRows")
	for _, want := range []string{"r.people", "p.text", "r.held", "h.text", "r.checks", `"check." + ck.check`, `"hold." + h.reason`} {
		if !strings.Contains(rows, want) {
			t.Errorf("screenshotRows does not use %s", want)
		}
	}
}
