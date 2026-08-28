package retrieval_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
)

func load(t *testing.T) *retrieval.Index {
	t.Helper()
	c, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	return retrieval.NewIndex(c)
}

func TestCityAliasesResolve(t *testing.T) {
	// A wrong city silently returns nothing, which reads to the person as
	// "there is nothing for me" rather than "I misread your city".
	for _, in := range []string{"成都", "成都市", "chengdu", "CHENGDU", " 成都 "} {
		if got := retrieval.NormalizeCity(in); got != "成都" {
			t.Errorf("NormalizeCity(%q) = %q, want 成都", in, got)
		}
	}
	if got := retrieval.NormalizeCity("拉萨"); got != "拉萨" {
		t.Errorf("an unknown city must pass through unchanged so the answer can name it: got %q", got)
	}
}

func TestTokenizerHandlesBothScripts(t *testing.T) {
	toks := retrieval.Tokenize("CNC 培训 course")
	has := func(s string) bool {
		for _, tk := range toks {
			if tk == s {
				return true
			}
		}
		return false
	}
	if !has("cnc") || !has("course") {
		t.Errorf("latin words missing from %v", toks)
	}
	// CJK is indexed as bigrams: enough to match without word segmentation,
	// tight enough that a single shared character is not a match.
	if !has("培训") {
		t.Errorf("CJK bigram missing from %v", toks)
	}
	if has("培") {
		t.Errorf("single CJK characters must not be indexed inside a longer run: %v", toks)
	}
	// A genuinely one-character term is still findable.
	if one := retrieval.Tokenize("证"); len(one) != 1 || one[0] != "证" {
		t.Errorf("a one-character term was lost: %v", one)
	}
}

func TestFiltersAreHardNotSoft(t *testing.T) {
	idx := load(t)
	c0, _ := corpus.Load("../../data")
	// A LOCAL listing in the wrong city must not surface. Ranking a good text
	// match above a location filter is how somebody gets sent 400km away.
	for _, h := range idx.SearchOpportunities(retrieval.Query{Text: "数控 机加工", City: "拉萨"}) {
		o, _ := c0.Opportunity(h.ID)
		if o.City != "" {
			t.Errorf("city filter leaked: local record %s (%s) returned for 拉萨", o.ID, o.City)
		}
	}
	hits := idx.SearchOpportunities(retrieval.Query{
		Text: "培训 课程", City: "成都", Kinds: []domain.OpportunityKind{domain.KindTraining},
	})
	if len(hits) == 0 {
		t.Fatal("no training results in Chengdu")
	}
	c, _ := corpus.Load("../../data")
	for _, h := range hits {
		o, ok := c.Opportunity(h.ID)
		if !ok {
			t.Fatalf("hit %q is not in the corpus", h.ID)
		}
		if o.Kind != domain.KindTraining {
			t.Errorf("kind filter leaked: %s is %s", o.ID, o.Kind)
		}
	}
}

func TestRankingExplainsItself(t *testing.T) {
	idx := load(t)
	hits := idx.SearchOpportunities(retrieval.Query{
		Text: "养老 护理 白班", City: "成都",
		Skills: []string{"养老照护"}, Cohorts: []domain.CohortTag{domain.CohortCaregiver},
	})
	if len(hits) == 0 {
		t.Fatal("no results")
	}
	// Every structured boost must be nameable, because the answer has to be able
	// to say why a listing was suggested to this person.
	var explained bool
	for _, h := range hits {
		if len(h.Reasons) > 0 {
			explained = true
		}
	}
	if !explained {
		t.Error("no hit carried a reason; ranking must be explainable")
	}
}

// The whole point of the national layer: somebody in a city the corpus has no
// local listings for still gets the framework that genuinely applies to them,
// instead of being told there is nothing.
func TestNationalRecordsReachEveryCity(t *testing.T) {
	idx := load(t)
	c, _ := corpus.Load("../../data")
	hits := idx.SearchOpportunities(retrieval.Query{Text: "失业保险金 培训补贴 社保", City: "深圳"})
	if len(hits) == 0 {
		t.Fatal("a city with no local listings got nothing at all")
	}
	for _, h := range hits {
		o, _ := c.Opportunity(h.ID)
		if o.City != "" {
			t.Errorf("%s is a 成都 listing but was returned for 深圳", o.ID)
		}
		if o.Scope != "national" {
			t.Errorf("%s reached another city without being marked national", o.ID)
		}
	}
}

// Where both apply, the local listing outranks the national framework: a named
// employer with an address is more actionable than a policy summary.
func TestLocalListingsOutrankTheNationalFramework(t *testing.T) {
	idx := load(t)
	c, _ := corpus.Load("../../data")
	hits := idx.SearchOpportunities(retrieval.Query{Text: "培训 补贴", City: "成都"})
	if len(hits) < 2 {
		t.Fatalf("expected both scopes, got %d hits", len(hits))
	}
	top, _ := c.Opportunity(hits[0].ID)
	if top.City == "" {
		t.Errorf("a national record outranked every local listing in a covered city (%s)", top.ID)
	}
}

func TestKnowledgeWithoutCityIsNationalGuidance(t *testing.T) {
	idx := load(t)
	hits := idx.SearchKnowledge(retrieval.Query{Text: "社保 转移 连续 月数", City: "深圳"})
	if len(hits) == 0 {
		t.Fatal("no knowledge results")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.ID, "kb-002") {
			found = true
		}
	}
	if !found {
		t.Errorf("kb-002 is national guidance and must reach a city with no local listings, got %v", hits)
	}
}
