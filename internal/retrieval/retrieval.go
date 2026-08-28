// Package retrieval is the RAG layer (step 10 of the build flow).
//
// Why retrieval rather than a big prompt: the opportunity and policy corpus
// changes on its own schedule and is far larger than any context window worth
// paying for. More importantly, a retrieved record carries a source_ref, and the
// source_ref is what lets the answer be checked. Content pasted into a prompt
// loses its provenance the moment the model paraphrases it.
//
// The scorer is BM25 over a tokenizer that handles both Latin words and CJK
// character bigrams, with hard metadata filters applied before scoring. It is
// deliberately lexical: an operator can be shown exactly why a record ranked
// where it did, which an embedding cannot do.
package retrieval

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

// cityAliases maps what people actually type to the city key used in the
// corpus. A table, not a heuristic: a wrong guess here silently returns "no
// results in your city", which reads to the user as "there is nothing for me".
var cityAliases = map[string]string{
	"成都市": "成都", "chengdu": "成都", "cd": "成都",
	"重庆市": "重庆", "chongqing": "重庆",
	"北京市": "北京", "beijing": "北京",
	"上海市": "上海", "shanghai": "上海",
	"广州市": "广州", "guangzhou": "广州",
	"深圳市": "深圳", "shenzhen": "深圳",
	"杭州市": "杭州", "hangzhou": "杭州",
	"西安市": "西安", "xian": "西安", "xi'an": "西安",
	"武汉市": "武汉", "wuhan": "武汉",
	"郑州市": "郑州", "zhengzhou": "郑州",
}

// NormalizeCity resolves an alias, returning the input unchanged when unknown so
// that the caller can report "no data for <what the user said>" truthfully.
func NormalizeCity(in string) string {
	k := strings.ToLower(strings.TrimSpace(in))
	if v, ok := cityAliases[k]; ok {
		return v
	}
	if v, ok := cityAliases[strings.TrimSpace(in)]; ok {
		return v
	}
	return strings.TrimSpace(in)
}

// CityNames returns every spelling of a city this build recognises, canonical
// first. An English-language answer writes "Chengdu" where the corpus says
// 成都; a check that only knows the canonical form would call that a miss.
func CityNames(city string) []string {
	canonical := NormalizeCity(city)
	if canonical == "" {
		return nil
	}
	out := []string{canonical}
	seen := map[string]bool{strings.ToLower(canonical): true}
	for alias, target := range cityAliases {
		if target != canonical || seen[strings.ToLower(alias)] {
			continue
		}
		seen[strings.ToLower(alias)] = true
		out = append(out, alias)
	}
	sort.Strings(out[1:])
	return out
}

// Query is a retrieval request. Filters are hard: they cut the candidate set
// before scoring, so a city filter can never be out-ranked by a good text match
// in the wrong city.
type Query struct {
	Text     string
	City     string
	District string
	Kinds    []domain.OpportunityKind
	Skills   []string
	Sectors  []string
	Cohorts  []domain.CohortTag
	Domains  []string // for knowledge docs
	Limit    int
}

// Hit carries the score and, more usefully, why it scored.
type Hit struct {
	ID        string   `json:"id"`
	SourceRef string   `json:"source_ref"`
	Score     float64  `json:"score"`
	Matched   []string `json:"matched_terms,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
}

type Index struct {
	c *corpus.Corpus

	oppDocs  []indexed
	knowDocs []indexed
	oppAvg   float64
	knowAvg  float64
	oppDF    map[string]int
	knowDF   map[string]int
}

type indexed struct {
	id     string
	ref    string
	terms  map[string]int
	length int
}

func NewIndex(c *corpus.Corpus) *Index {
	idx := &Index{c: c, oppDF: map[string]int{}, knowDF: map[string]int{}}
	for _, o := range c.Opportunities {
		text := strings.Join([]string{
			o.Title, o.Summary, o.Org, o.City, o.District,
			strings.Join(o.Skills, " "), strings.Join(o.Sectors, " "),
			strings.Join(cohortStrings(o.Cohorts), " "), o.Schedule, o.Amount,
			criteriaText(o.Criteria),
		}, " ")
		idx.oppDocs = append(idx.oppDocs, mkIndexed(o.ID, o.SourceRef, text))
	}
	for _, d := range c.Docs {
		text := strings.Join([]string{d.Title, d.Body, strings.Join(d.Domains, " "), strings.Join(d.Cohorts, " "), d.City}, " ")
		idx.knowDocs = append(idx.knowDocs, mkIndexed(d.ID, d.SourceRef, text))
	}
	idx.oppAvg, idx.oppDF = stats(idx.oppDocs)
	idx.knowAvg, idx.knowDF = stats(idx.knowDocs)
	return idx
}

func criteriaText(cs []domain.Criterion) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Text)
		b.WriteByte(' ')
	}
	return b.String()
}

func cohortStrings(in []domain.CohortTag) []string {
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = string(c)
	}
	return out
}

func mkIndexed(id, ref, text string) indexed {
	terms := map[string]int{}
	toks := Tokenize(text)
	for _, t := range toks {
		terms[t]++
	}
	return indexed{id: id, ref: ref, terms: terms, length: len(toks)}
}

func stats(docs []indexed) (float64, map[string]int) {
	df := map[string]int{}
	total := 0
	for _, d := range docs {
		total += d.length
		for t := range d.terms {
			df[t]++
		}
	}
	if len(docs) == 0 {
		return 1, df
	}
	return float64(total) / float64(len(docs)), df
}

// Tokenize splits Latin words on non-alphanumerics and emits CJK character
// BIGRAMS, so a Chinese query matches Chinese text without a word-segmentation
// dependency. Latin terms are lowercased.
//
// Bigrams only, deliberately. Indexing single characters as well looks more
// forgiving and is in fact much worse: 接 in 接送孩子 matched a query about
// 焊接, and one stray character is enough to turn "nothing found" into a
// confident wrong result. A run of exactly one CJK character still emits that
// character, so a genuinely one-character term is not lost.
func Tokenize(s string) []string {
	var out []string
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() > 0 {
			out = append(out, strings.ToLower(latin.String()))
			latin.Reset()
		}
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			out = append(out, string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return out
}

const (
	bm25K1 = 1.4
	bm25B  = 0.75
)

func score(q []string, d indexed, df map[string]int, n int, avg float64) (float64, []string) {
	var s float64
	var matched []string
	seen := map[string]bool{}
	for _, term := range q {
		tf, ok := d.terms[term]
		if !ok {
			continue
		}
		if !seen[term] {
			matched = append(matched, term)
			seen[term] = true
		}
		idf := math.Log(1 + (float64(n)-float64(df[term])+0.5)/(float64(df[term])+0.5))
		norm := float64(tf) * (bm25K1 + 1) /
			(float64(tf) + bm25K1*(1-bm25B+bm25B*float64(d.length)/avg))
		s += idf * norm
	}
	return s, matched
}

// SearchOpportunities applies the hard filters, scores what survives, and
// returns hits with the reasons that produced them.
func (idx *Index) SearchOpportunities(q Query) []Hit {
	city := NormalizeCity(q.City)
	terms := Tokenize(q.Text)
	limit := q.Limit
	if limit <= 0 {
		limit = 8
	}
	var hits []Hit
	for i, o := range idx.c.Opportunities {
		// A record with no city is national and applies everywhere. Filtering it
		// out by city would tell somebody in an uncovered city that nothing
		// exists for them, which is not true.
		national := o.City == ""
		if city != "" && !national && !strings.EqualFold(o.City, city) {
			continue
		}
		if q.District != "" && o.District != "" && !strings.EqualFold(o.District, q.District) {
			continue
		}
		if len(q.Kinds) > 0 && !containsKind(q.Kinds, o.Kind) {
			continue
		}
		s, matched := score(terms, idx.oppDocs[i], idx.oppDF, len(idx.oppDocs), idx.oppAvg)
		var reasons []string
		if national {
			reasons = append(reasons, "全国通用，任何城市都适用")
		} else if city != "" {
			reasons = append(reasons, "本地记录")
		}
		// Structured overlaps are additive on top of the text score, and each
		// one is named. A user who asks "why this job" gets these lines back.
		if n := overlap(q.Skills, o.Skills); n > 0 {
			s += 1.6 * float64(n)
			reasons = append(reasons, "matches stated skills: "+strings.Join(intersect(q.Skills, o.Skills), ", "))
		}
		if n := overlap(q.Sectors, o.Sectors); n > 0 {
			s += 1.0 * float64(n)
			reasons = append(reasons, "in the requested sector")
		}
		if n := overlapCohorts(q.Cohorts, o.Cohorts); n > 0 {
			// Cohort overlap only ever adds. It is never used to subtract, which
			// is the difference between support and profiling.
			s += 1.2 * float64(n)
			reasons = append(reasons, "designed for the stated situation")
		}
		if s <= 0 {
			continue
		}
		if national && city != "" {
			// Where both apply, the local listing goes first: a named employer
			// with an address and opening hours is more actionable than a policy
			// framework. Multiplicative rather than a fixed subtraction, so it
			// holds at any BM25 scale — but small enough that a national record
			// with a much better text match can still surface above a weak local
			// one, which is the right outcome when only it answers the question.
			s *= 0.7
		}
		hits = append(hits, Hit{ID: o.ID, SourceRef: o.SourceRef, Score: round(s), Matched: matched, Reasons: reasons})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func (idx *Index) SearchKnowledge(q Query) []Hit {
	terms := Tokenize(q.Text)
	limit := q.Limit
	if limit <= 0 {
		limit = 4
	}
	city := NormalizeCity(q.City)
	var hits []Hit
	for i, d := range idx.c.Docs {
		if len(q.Domains) > 0 && !overlapStrings(q.Domains, d.Domains) {
			continue
		}
		// A doc with no city is national guidance and always applies.
		if city != "" && d.City != "" && !strings.EqualFold(d.City, city) {
			continue
		}
		s, matched := score(terms, idx.knowDocs[i], idx.knowDF, len(idx.knowDocs), idx.knowAvg)
		if len(q.Cohorts) > 0 && overlapStrings(cohortStrings(q.Cohorts), d.Cohorts) {
			s += 1.2
		}
		if s <= 0 {
			continue
		}
		hits = append(hits, Hit{ID: d.ID, SourceRef: d.SourceRef, Score: round(s), Matched: matched})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func containsKind(ks []domain.OpportunityKind, k domain.OpportunityKind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func overlap(a, b []string) int { return len(intersect(a, b)) }

func intersect(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range b {
		set[strings.ToLower(strings.TrimSpace(x))] = true
	}
	var out []string
	for _, x := range a {
		k := strings.ToLower(strings.TrimSpace(x))
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

func overlapStrings(a, b []string) bool { return overlap(a, b) > 0 }

func overlapCohorts(a []domain.CohortTag, b []domain.CohortTag) int {
	return overlap(cohortStrings(a), cohortStrings(b))
}

func round(f float64) float64 { return math.Round(f*1000) / 1000 }
