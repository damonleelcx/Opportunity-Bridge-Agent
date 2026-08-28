// Package corpus loads the opportunity and knowledge records the agent is
// allowed to talk about.
//
// The rule the whole product rests on: the agent may name a program only if
// that program is in the corpus, and it must show the corpus record's
// source_ref when it does. That is what turns "sounds plausible" into
// "checkable". The no_invented_identifiers verifier enforces it against this
// index.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

// Doc is a retrievable prose document: a procedure guide or policy explainer.
type Doc struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Domains   []string `json:"domains,omitempty"`
	Cohorts   []string `json:"cohorts,omitempty"`
	City      string   `json:"city,omitempty"`
	SourceRef string   `json:"source_ref"`
}

type Corpus struct {
	Opportunities []domain.Opportunity
	Docs          []Doc

	byID    map[string]domain.Opportunity
	refs    map[string]bool // every source_ref and every id, for the verifier
	docByID map[string]Doc
}

// Load reads the three corpus files. A missing file is an error rather than a
// warning: an agent with an empty corpus cannot cite anything, and would spend
// the whole conversation apologising. Failing at startup is the honest outcome.
func Load(dir string) (*Corpus, error) {
	c := &Corpus{
		byID:    map[string]domain.Opportunity{},
		refs:    map[string]bool{},
		docByID: map[string]Doc{},
	}
	if err := readJSON(filepath.Join(dir, "opportunities.json"), &c.Opportunities); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "knowledge.json"), &c.Docs); err != nil {
		return nil, err
	}
	if len(c.Opportunities) == 0 {
		return nil, fmt.Errorf("CORPUS_EMPTY: %s/opportunities.json contains no records; the agent would have nothing it is allowed to cite", dir)
	}
	for _, o := range c.Opportunities {
		if o.ID == "" || o.SourceRef == "" {
			return nil, fmt.Errorf("CORPUS_INVALID: opportunity %q is missing id or source_ref; every record must be citable", o.Title)
		}
		c.byID[o.ID] = o
		c.refs[o.ID] = true
		c.refs[o.SourceRef] = true
		for _, cr := range o.Criteria {
			if cr.SourceRef != "" {
				c.refs[cr.SourceRef] = true
			}
		}
	}
	for _, d := range c.Docs {
		if d.ID == "" || d.SourceRef == "" {
			return nil, fmt.Errorf("CORPUS_INVALID: knowledge doc %q is missing id or source_ref", d.Title)
		}
		c.docByID[d.ID] = d
		c.refs[d.ID] = true
		c.refs[d.SourceRef] = true
	}
	return c, nil
}

// LoadSignals reads the seed demand signals. Unlike the corpus proper, an
// absent file is fine - a fresh install simply has no history yet.
func LoadSignals(dir string) ([]domain.DemandSignal, error) {
	var out []domain.DemandSignal
	path := filepath.Join(dir, "signals.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	if err := readJSON(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func readJSON(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("CORPUS_READ_FAILED: cannot read %s: %w; set OBA_CORPUS_DIR to a directory containing opportunities.json and knowledge.json", path, err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("CORPUS_PARSE_FAILED: %s is not valid JSON: %w", path, err)
	}
	return nil
}

func (c *Corpus) Opportunity(id string) (domain.Opportunity, bool) {
	o, ok := c.byID[id]
	return o, ok
}

func (c *Corpus) Doc(id string) (Doc, bool) {
	d, ok := c.docByID[id]
	return d, ok
}

// KnownRef reports whether a string names something in the corpus. Used by the
// no_invented_identifiers verifier.
func (c *Corpus) KnownRef(ref string) bool { return c.refs[strings.TrimSpace(ref)] }

// Refs returns every citable identifier, sorted. Exposed for the verifier and
// for the eval suite.
func (c *Corpus) Refs() []string {
	out := make([]string, 0, len(c.refs))
	for r := range c.refs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Cities lists the cities present, so the UI can say what the demo covers
// instead of letting a user search a city with no data and conclude the product
// is broken.
func (c *Corpus) Cities() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range c.Opportunities {
		if o.City != "" && !seen[o.City] {
			seen[o.City] = true
			out = append(out, o.City)
		}
	}
	sort.Strings(out)
	return out
}

// IDPrefixes returns every id prefix present in the corpus, sorted.
//
// It exists so a test can assert that the citation regex in package guardrail
// knows about all of them. Adding a new record family used to be a silent
// failure: correctly-cited answers looked uncited, got force-redrafted, and came
// back worse than the draft they replaced.
func (c *Corpus) IDPrefixes() []string {
	seen := map[string]bool{}
	collect := func(id string) {
		if i := strings.Index(id, "-"); i > 0 {
			seen[id[:i]] = true
		}
	}
	for _, o := range c.Opportunities {
		collect(o.ID)
	}
	for _, d := range c.Docs {
		collect(d.ID)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
