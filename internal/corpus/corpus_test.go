package corpus_test

import (
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
)

// Both READMEs claimed "the sample corpus is written in English". It is not, and
// had not been for some time — every title, org and address in data/ is Chinese.
//
// This is the third stale claim found in the same honest-limits list (the corpus
// count said 21 against 26; the read-aloud card promised no audio left the
// device). The other two now derive from the running instance. This one cannot —
// it is prose about the shape of the data — so it gets the next best thing: a
// test that fails when the documents and the data disagree.
// See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
func TestDocsAgreeWithTheLanguageOfTheCorpus(t *testing.T) {
	// ../../data on purpose, not the fixture: this fence is about what SHIPS.
	// The invented records moved to testdata/corpus when they left the product;
	// see docs/bugfix/2026-08-31-the-invented-corpus-left-the-product.md
	c, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}

	cjk, total := 0, 0
	for _, o := range c.Opportunities {
		for _, r := range o.Title + o.Org {
			if unicode.IsLetter(r) {
				total++
				if unicode.Is(unicode.Han, r) {
					cjk++
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("no letters in any opportunity title or org; this fence no longer measures anything")
	}
	chinese := float64(cjk)/float64(total) > 0.5

	for _, path := range []string{"../../README.md", "../../README.zh-CN.md"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		doc := string(b)
		for _, claim := range []string{"sample corpus is written in English", "样例语料是英文写的"} {
			if strings.Contains(doc, claim) && chinese {
				t.Errorf("%s says %q, but %d%% of the corpus's titles and orgs are Han characters",
					path, claim, cjk*100/total)
			}
		}
	}
}

// The badge follows the data, and the shipped corpus must not carry it.
//
// `corpus_is_sample` was a literal `true` in the HTTP layer. Correct while every
// record was invented; a lie the moment they left, because a permanent
// 「演示语料」 badge over five real national schemes tells somebody not to act on
// the only things here they CAN act on.
// See docs/bugfix/2026-08-31-the-invented-corpus-left-the-product.md
func TestShippedCorpusCarriesNoInventedRecords(t *testing.T) {
	shipped, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("shipped corpus: %v", err)
	}
	for _, o := range shipped.Opportunities {
		if strings.HasPrefix(o.SourceRef, "SAMPLE/") {
			t.Errorf("%s (%s) still cites %s: an invented record is being shipped",
				o.ID, o.Title, o.SourceRef)
		}
		if !strings.HasPrefix(o.SourceRef, "http") {
			t.Errorf("%s cites %q, which nobody can open and check", o.ID, o.SourceRef)
		}
	}
	if shipped.IsSample() {
		t.Error("the shipped corpus reports itself as sample data, so every reader gets the 演示语料 badge")
	}

	// And the fixture, which the tests and the demo run against, must still be
	// the invented one — otherwise this fence is comparing nothing.
	fixture, err := corpus.Load("../../testdata/corpus")
	if err != nil {
		t.Fatalf("fixture corpus: %v", err)
	}
	if !fixture.IsSample() {
		t.Error("the fixture corpus no longer contains invented records; either it moved or this fence is vacuous")
	}
}
