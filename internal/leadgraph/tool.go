package leadgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The action surface a model may reach, and the contract it is held to.
//
// WHY THIS PACKAGE CARRIES ITS OWN SCHEMA SUBSET
//
//	The sibling product has one (internal/tools), and importing it would drag
//	in its consent scopes, its roles and its intent registry - the exact things
//	§01 of the PRD says not to reuse, because the two products' boundaries
//	contradict each other. Duplicating ~150 lines of JSON Schema is the smaller
//	cost, and it is the piece that becomes a shared library the day Q4 is
//	decided in favour of separate repositories. Recorded here as a known cost
//	rather than left to be discovered.
//
// WHAT THE SCHEMA IS ACTUALLY FOR
//
//	Every tool sets additionalProperties:false, and arguments are validated
//	BEFORE Run is entered. That is what makes "the model cannot smuggle in a
//	match score" true structurally: a call carrying `匹配度` is refused at the
//	door, not inside the function where somebody has to remember to check.
//
// WHY IRREVERSIBLE TOOLS TAKE AN APPROVAL BOUND TO THE ARGUMENTS
//
//	An approval for "delete something" is not an approval for "delete this".
//	The token carries a hash of the exact arguments, so a human saw the same
//	call that runs. See docs/20-lead-graph.zh-CN.md §05.2.

// Risk says what a tool can do to the world.
type Risk string

const (
	RiskRead         Risk = "read"
	RiskWrite        Risk = "write"
	RiskIrreversible Risk = "irreversible"
)

// Schema is a deliberately small JSON Schema subset. Anything it does not
// understand is rejected loudly rather than waved through.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
}

func no() *bool            { b := false; return &b }
func f(v float64) *float64 { return &v }

// Obj always closes the object. There is no constructor that leaves it open:
// forgetting additionalProperties is the mistake this prevents.
func Obj(desc string, props map[string]*Schema, required ...string) *Schema {
	sort.Strings(required)
	return &Schema{Type: "object", Description: desc, Properties: props,
		Required: required, AdditionalProperties: no()}
}
func Str(desc string, enum ...string) *Schema {
	return &Schema{Type: "string", Description: desc, Enum: enum}
}
func Int(desc string, min, max float64) *Schema {
	return &Schema{Type: "integer", Description: desc, Minimum: f(min), Maximum: f(max)}
}
func Bool(desc string) *Schema { return &Schema{Type: "boolean", Description: desc} }
func Arr(desc string, of *Schema) *Schema {
	return &Schema{Type: "array", Description: desc, Items: of}
}

var (
	ErrUnknownTool     = errors.New("UNKNOWN_TOOL: no tool by that name")
	ErrBadArguments    = errors.New("BAD_ARGUMENTS: the call does not match the tool's schema")
	ErrApprovalMissing = errors.New("APPROVAL_MISSING: this tool cannot run without a human approval of these exact arguments")
)

// Validate checks args against the schema. Returns the first problem, named, so
// the model can correct one thing rather than guess.
func (s *Schema) Validate(path string, v any) error {
	if s == nil {
		return nil
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s must be an object", ErrBadArguments, path)
		}
		for _, r := range s.Required {
			if _, present := m[r]; !present {
				return fmt.Errorf("%w: %s.%s is required", ErrBadArguments, path, r)
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub, known := s.Properties[k]
			if !known {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("%w: %s.%s is not a field this tool accepts", ErrBadArguments, path, k)
				}
				continue
			}
			if err := sub.Validate(path+"."+k, m[k]); err != nil {
				return err
			}
		}
	case "array":
		xs, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%w: %s must be an array", ErrBadArguments, path)
		}
		for i, x := range xs {
			if err := s.Items.Validate(fmt.Sprintf("%s[%d]", path, i), x); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%w: %s must be a string", ErrBadArguments, path)
		}
		if len(s.Enum) > 0 {
			for _, e := range s.Enum {
				if str == e {
					return nil
				}
			}
			return fmt.Errorf("%w: %s must be one of %s", ErrBadArguments, path, strings.Join(s.Enum, ", "))
		}
	case "integer":
		n, ok := numberOf(v)
		if !ok || n != float64(int(n)) {
			return fmt.Errorf("%w: %s must be a whole number", ErrBadArguments, path)
		}
		if s.Minimum != nil && n < *s.Minimum {
			return fmt.Errorf("%w: %s must be at least %v", ErrBadArguments, path, *s.Minimum)
		}
		if s.Maximum != nil && n > *s.Maximum {
			return fmt.Errorf("%w: %s must be at most %v", ErrBadArguments, path, *s.Maximum)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%w: %s must be true or false", ErrBadArguments, path)
		}
	}
	return nil
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		x, err := n.Float64()
		return x, err == nil
	}
	return 0, false
}

// Tool is one thing a model may do.
type Tool struct {
	Name        string
	Description string
	Schema      *Schema
	Risk        Risk
	Run         func(s *Store, v View, args map[string]any) (any, error)
}

// Approval is a human's yes to one specific call.
type Approval struct {
	Tool string
	Hash string
}

// ApprovalFor is the hash a caller must show. Arguments are canonicalised
// through JSON so that key order cannot change the hash.
func ApprovalFor(tool string, args map[string]any) Approval {
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(append([]byte(tool+"\x00"), raw...))
	return Approval{Tool: tool, Hash: hex.EncodeToString(sum[:])}
}

// Registry is the closed set of tools, in a stable order.
type Registry struct {
	byName map[string]Tool
	order  []string
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{byName: map[string]Tool{}}
	for _, t := range ts {
		r.byName[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	sort.Strings(r.order)
	return r
}

func (r *Registry) Names() []string { return append([]string{}, r.order...) }

func (r *Registry) Get(name string) (Tool, bool) { t, ok := r.byName[name]; return t, ok }

// Call validates, checks the approval, and only then runs.
//
// The order matters and is the whole point: a call that fails validation never
// reaches Run, and an irreversible call without a matching approval never
// reaches it either.
func (r *Registry) Call(s *Store, v View, name string, args map[string]any, approvals []Approval) (any, error) {
	t, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := t.Schema.Validate(name, args); err != nil {
		return nil, err
	}
	if t.Risk == RiskIrreversible {
		want := ApprovalFor(name, args)
		var found bool
		for _, a := range approvals {
			if a.Tool == want.Tool && a.Hash == want.Hash {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", ErrApprovalMissing, name)
		}
	}
	return t.Run(s, v, args)
}

// ---- argument helpers ----

func argStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, k string) int {
	if n, ok := numberOf(args[k]); ok {
		return int(n)
	}
	return 0
}

func argStrs(args map[string]any, k string) []string {
	xs, _ := args[k].([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func argInts(args map[string]any, k string) []int {
	xs, _ := args[k].([]any)
	out := make([]int, 0, len(xs))
	for _, x := range xs {
		if n, ok := numberOf(x); ok {
			out = append(out, int(n))
		}
	}
	return out
}

func argMaps(args map[string]any, k string) []map[string]any {
	xs, _ := args[k].([]any)
	out := make([]map[string]any, 0, len(xs))
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func argTime(args map[string]any, k string) *time.Time {
	s := argStr(args, k)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// nodeFromArgs builds a candidate from validated arguments. It never invents an
// Intel: a candidate with no evidence is refused by the store, which is where
// that rule belongs.
func nodeFromArgs(m map[string]any) Node {
	n := Node{
		Kind: NodeKind(argStr(m, "kind")), Label: argStr(m, "label"), Org: argStr(m, "org"),
		UnitPath: argStrs(m, "unit_path"), RoleTitle: argStr(m, "role_title"),
		Duty: argStr(m, "duty"), Note: argStr(m, "note"), OccurredAt: argTime(m, "occurred_at"),
	}
	for _, cm := range argMaps(m, "contacts") {
		n.Contacts = append(n.Contacts, ContactPoint{
			Kind: ContactKind(argStr(cm, "kind")), Value: argStr(cm, "value"),
		})
	}
	for _, im := range argMaps(m, "intel") {
		announced, _ := im["announced"].(bool)
		n.Intel = append(n.Intel, Intel{
			Kind: IntelKind(argStr(im, "kind")), SourceURL: argStr(im, "source_url"),
			TurnRef: argStr(im, "turn_ref"), Excerpt: argStr(im, "excerpt"), Announced: announced,
		})
	}
	return n
}

func linkFromArgs(m map[string]any) LinkInput {
	end := func(k string) Endpoint {
		em, _ := m[k].(map[string]any)
		if em == nil {
			return Endpoint{}
		}
		return Endpoint{Label: argStr(em, "label"), Org: argStr(em, "org")}
	}
	l := LinkInput{
		Kind: EdgeKind(argStr(m, "kind")), From: end("from"), To: end("to"),
		Context: argStr(m, "context"),
	}
	for _, im := range argMaps(m, "intel") {
		announced, _ := im["announced"].(bool)
		l.Intel = append(l.Intel, Intel{
			Kind: IntelKind(argStr(im, "kind")), SourceURL: argStr(im, "source_url"),
			TurnRef: argStr(im, "turn_ref"), Excerpt: argStr(im, "excerpt"), Announced: announced,
		})
	}
	return l
}

var intelSchema = Obj("where this fact came from; a record without one is refused",
	map[string]*Schema{
		"kind":       Str("who is speaking", string(IntelUserSaid), string(IntelPublicSource), string(IntelTeamShared)),
		"source_url": Str("required for a public source"),
		"turn_ref":   Str("the conversation turn, for something the user said"),
		"excerpt":    Str("the words themselves, never a summary"),
		"announced":  Bool("true only for an official filing or the company's own notice"),
	}, "kind", "excerpt")

var candidateSchema = Obj("one fact extracted from what was said",
	map[string]*Schema{
		"kind":        Str("what this is", string(KindOrg), string(KindUnit), string(KindPerson), string(KindEvent), string(KindLead)),
		"label":       Str("the name a human would use"),
		"org":         Str("the company"),
		"unit_path":   Arr("where it sits, e.g. [A司, 技术中心, c业务组]", Str("one level")),
		"role_title":  Str("their job title, if stated"),
		"duty":        Str("what they actually look after, if stated"),
		"occurred_at": Str("when an event happened, RFC3339 or YYYY-MM-DD"),
		"note":        Str("your own private note; never shared with teammates"),
		"contacts":    Arr("how to reach them, if they said", contactSchema),
		"intel":       Arr("the evidence", intelSchema),
	}, "kind", "label", "intel")

// linkSchema is a relationship line. Its ends are named by company and person,
// because the model cannot know the id of somebody it is describing for the
// first time — and the line and the two people almost always arrive in one
// sentence, so needing ids would mean needing a second turn.
//
// There is no strength field, on purpose: how well two people know each other
// is a judgement only a person may record. See Endpoint.
var linkSchema = Obj("one relationship between two people",
	map[string]*Schema{
		"kind": Str("what kind of line", string(EdgeBelongsTo), string(EdgeReportsTo),
			string(EdgeColleague), string(EdgeKnows), string(EdgeReferredBy), string(EdgeInfluences)),
		"from":    endpointSchema,
		"to":      endpointSchema,
		"context": Str("what makes them connected, in the user's own words"),
		"intel":   Arr("the evidence", intelSchema),
	}, "kind", "from", "to", "intel")

var endpointSchema = Obj("one end of a relationship",
	map[string]*Schema{
		"label": Str("the person's name, exactly as you recorded them this turn"),
		"org":   Str("their company, which is what tells two people of the same name apart"),
	}, "label")

var contactSchema = Obj("one way to reach somebody",
	map[string]*Schema{
		"kind":  Str("the channel", string(ContactPhone), string(ContactEmail), string(ContactWeChat), string(ContactOther)),
		"value": Str("the number, address or id as they gave it"),
	}, "kind", "value")

// Tools is the action surface. Deliberately closed and deliberately small:
// every entry is something the model can be held to, and there is no
// general-purpose write.
func Tools() *Registry {
	return NewRegistry(
		Tool{
			Name: "graph_reconcile", Risk: RiskRead,
			Description: "Compare facts you just heard against the graph. Writes nothing. Returns, per candidate, whether it is new, an update, a possible duplicate, or a contradiction a person must settle.",
			Schema: Obj("facts to compare", map[string]*Schema{
				"candidates": Arr("what you heard", candidateSchema),
			}, "candidates"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				var cs []Node
				for _, m := range argMaps(a, "candidates") {
					cs = append(cs, nodeFromArgs(m))
				}
				return s.Reconcile(v, cs), nil
			},
		},
		Tool{
			Name: "record_turn", Risk: RiskWrite,
			Description: "Record what the user just told you: reconcile it, write what needs no judgement, park what does, and return one line of receipt plus at most one question.",
			Schema: Obj("this turn's facts", map[string]*Schema{
				"turn_ref":   Str("identifier of this conversation turn"),
				"candidates": Arr("what you heard", candidateSchema),
				"links": Arr("who is connected to whom, when they said so. Both ends must be "+
					"among the candidates above or already recorded", linkSchema),
			}, "turn_ref"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				in := TurnInput{TurnRef: argStr(a, "turn_ref")}
				for _, m := range argMaps(a, "candidates") {
					in.Nodes = append(in.Nodes, nodeFromArgs(m))
				}
				for _, m := range argMaps(a, "links") {
					in.Links = append(in.Links, linkFromArgs(m))
				}
				return s.RecordTurn(v, ActorAgent, in)
			},
		},
		Tool{
			Name: "answer_pending", Risk: RiskWrite,
			Description: "Apply the user's answer to a question the graph asked earlier. Only the person who was asked may answer.",
			Schema: Obj("the answer", map[string]*Schema{
				"pending_id":    Str("the question being answered"),
				"choice":        Str("what they decided", string(ChooseCreateNew), string(ChooseMergeInto), string(ChooseAcceptChange), string(ChooseKeepCurrent)),
				"merge_into_id": Str("required when the choice is merge_into"),
			}, "pending_id", "choice"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return s.Answer(v, ActorUser, argStr(a, "pending_id"), Resolution{
					Choice: Choice(argStr(a, "choice")), MergeIntoID: argStr(a, "merge_into_id"),
				})
			},
		},
		Tool{
			Name: "touchpoint_add", Risk: RiskWrite,
			Description: "Record one contact with one person: when, how, what was discussed, what came of it, and what happens next.",
			Schema: Obj("the contact", map[string]*Schema{
				"person_id": Str("who was contacted"),
				"at":        Str("when it happened, RFC3339 or YYYY-MM-DD"),
				"via":       Str("how", string(ChannelPhone), string(ChannelMessage), string(ChannelEmail), string(ChannelInPerson), string(ChannelOther)),
				"topic":     Str("what was discussed - a fact, shared with the team"),
				"outcome":   Str("what was said - a fact, shared with the team"),
				"next_step": Str("what happens next"),
				"due_at":    Str("when the next step is due"),
				"note":      Str("your own impression; never shared with teammates"),
			}, "person_id", "at", "via"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				when := argTime(a, "at")
				if when == nil {
					return nil, ErrTouchpointTimeRequired
				}
				return s.AddTouchpoint(v, ActorAgent, Touchpoint{
					PersonID: argStr(a, "person_id"), At: *when, Via: Channel(argStr(a, "via")),
					Topic: argStr(a, "topic"), Outcome: argStr(a, "outcome"),
					NextStep: argStr(a, "next_step"), DueAt: argTime(a, "due_at"), Note: argStr(a, "note"),
				})
			},
		},
		Tool{
			Name: "graph_query", Risk: RiskRead,
			Description: "Look up what the team already knows: people, groups, companies or events, optionally inside one company or one group.",
			Schema: Obj("the query", map[string]*Schema{
				"kind":      Str("narrow by kind", string(KindOrg), string(KindUnit), string(KindPerson), string(KindEvent), string(KindLead)),
				"org":       Str("narrow to one company"),
				"unit_path": Arr("narrow to a group and everything under it", Str("one level")),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return s.Nodes(v, NodeFilter{
					Kind: NodeKind(argStr(a, "kind")), Org: argStr(a, "org"), UnitPath: argStrs(a, "unit_path"),
				}), nil
			},
		},
		Tool{
			Name: "org_chart", Risk: RiskRead,
			Description: "The structure of one company as this team knows it. Levels nobody recorded are marked as merely mentioned, and people nobody could place are listed rather than dropped.",
			Schema: Obj("which company", map[string]*Schema{
				"org": Str("the company"),
				"at":  Str("as of when, RFC3339 or YYYY-MM-DD; defaults to now"),
			}, "org"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				when := time.Time{}
				if t := argTime(a, "at"); t != nil {
					when = *t
				}
				return s.OrgChart(v, argStr(a, "org"), when), nil
			},
		},
		Tool{
			Name: "path_find", Risk: RiskRead,
			Description: "How to reach somebody through people you know. Returns routes ordered by their weakest link, not by how short they are, and says when you have not recorded who you know.",
			Schema: Obj("the target", map[string]*Schema{
				"target_id": Str("who you want to reach"),
				"max_hops":  Int("how far to look, 1-4", 1, 4),
				"max_paths": Int("how many routes to return, 1-5", 1, 5),
			}, "target_id"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return s.PathsTo(v, argStr(a, "target_id"), PathOptions{
					MaxHops: argInt(a, "max_hops"), MaxPaths: argInt(a, "max_paths"),
				}), nil
			},
		},
		Tool{
			Name: "lead_board", Risk: RiskRead,
			Description: "Which groups are worth time now, each with the named signals that make up its score. The subject is always a group, never a person.",
			Schema:      Obj("no arguments", map[string]*Schema{}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return s.LeadBoard(v, time.Time{}), nil
			},
		},
		Tool{
			Name: "stale_scan", Risk: RiskRead,
			Description: "What has gone quiet: fields still unconfirmed, people nobody has contacted in a long time, follow-ups now overdue.",
			Schema: Obj("how cold is cold", map[string]*Schema{
				"cold_days": Int("how many days without contact counts as cold, 7-365", 7, 365),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				days := argInt(a, "cold_days")
				if days == 0 {
					days = 90
				}
				return s.StaleScan(v, time.Now().UTC(), time.Duration(days)*24*time.Hour), nil
			},
		},
		Tool{
			Name: "import_summary", Risk: RiskRead,
			Description: "Describe a file the user uploaded and what importing it would do: how it was read, which columns were used, which were ignored, what will be created, what still needs their decision, and which rows were skipped and why. Writes nothing. " +
				"For a mind-map SCREENSHOT (source \"screenshot\") it also lists every person the model read, each beside the verbatim text it came from (people); people held because the picture says they have left (held); rows to look at twice, such as a name that is not written on its own in its topic (checks); and how many reporting lines committing would draw (links). Every one of those rows is a model's reading: go through them with the user, and never describe the reading as correct on their behalf.",
			Schema: Obj("which staged file", map[string]*Schema{
				"import_id": Str("the staged import; omit for the most recent one"),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				sum, ok := s.ImportSummary(v, argStr(a, "import_id"))
				if !ok {
					return nil, ErrNotFound
				}
				return sum, nil
			},
		},
		Tool{
			Name: "rating_gap", Risk: RiskRead,
			Description: "How many people the user has said they know, how many they have not, and a few to ask about. Use it after an import: with no relationship strength recorded, path_find has nowhere to start and the graph is full but useless.",
			Schema: Obj("how many to ask about", map[string]*Schema{
				"limit": Int("how many names to put in front of them at once, 1-25", 1, 25),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return s.RatingGap(v, argInt(a, "limit")), nil
			},
		},
		Tool{
			Name: "rate_contact", Risk: RiskWrite,
			Description: "Record how well the USER says they know somebody, 1 (barely) to 3 (well). Only ever relay what they told you - this number decides which introduction routes get offered, and a guess sends them down one that goes nowhere.",
			Schema: Obj("the rating", map[string]*Schema{
				"node_id":  Str("who"),
				"strength": Int("1 barely, 2 somewhat, 3 well", 1, 3),
				"note":     Str("their own words about this person, if they gave any; private to them"),
			}, "node_id", "strength"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				id := argStr(a, "node_id")
				if err := s.Annotate(v, ActorUser, id, argInt(a, "strength"), argStr(a, "note")); err != nil {
					return nil, err
				}
				n, _ := s.Node(v, id)
				return n, nil
			},
		},
		Tool{
			// RiskWrite, not read: it changes the staged reading. It writes
			// nothing to the graph, which is why it does not need an approval -
			// the approval comes later, on the commit, and covers the reading
			// this produced.
			Name: "import_remap", Risk: RiskWrite,
			Description: "Correct how a staged import is read, AFTER the user has confirmed the correction: which column is which, what its values mean (很熟 = 3), a value they supplied for a row that was missing one, and rows to leave out. Re-reads the whole import and returns the new plan. Propose first, look at the header and sample rows in import_summary, and never guess on the user's behalf: guessing that column 3 is the company imports two hundred wrong records that all look right. " +
				"For a mind-map SCREENSHOT the row is the topic number: `rows` also corrects a name or title the model read wrongly, and `include` imports somebody held because the picture says they have left - only when the user says so. A screenshot correction re-plans from the reading already staged; the picture is not read again.",
			Schema: Obj("the corrections", map[string]*Schema{
				"import_id": Str("the staged import; omit for the most recent one"),
				"columns": Arr("which column holds which field", Obj("one column", map[string]*Schema{
					"field":  Str("the field", string(ColLabel), string(ColOrg), string(ColUnit), string(ColRole), string(ColDuty), string(ColStrength), string(ColNote)),
					"header": Str("the column's header text, or #3 for the third column"),
				}, "field", "header")),
				"values": Arr("what this column's values mean", Obj("one translation", map[string]*Schema{
					"field": Str("the field", string(ColLabel), string(ColOrg), string(ColUnit), string(ColRole), string(ColDuty), string(ColStrength), string(ColNote)),
					"from":  Str("what the file says, e.g. 很熟"),
					"to":    Str("what it means here, e.g. 3"),
				}, "field", "from", "to")),
				"rows": Arr("a value the USER supplied: a cell missing from a file, or in a screenshot a name or title the model read wrongly", Obj("one cell", map[string]*Schema{
					// From 1, not 2: a screenshot's topics are numbered from 1. A
					// file's line 1 is its header, which no override touches.
					"row":   Int("the line or topic number as reported", 1, 1000000),
					"field": Str("the field", string(ColLabel), string(ColOrg), string(ColUnit), string(ColRole), string(ColDuty), string(ColStrength), string(ColNote)),
					"value": Str("what the user said it is - never what you inferred"),
				}, "row", "field", "value")),
				"ignore": Arr("line or topic numbers the user said to leave out", Int("a line or topic number", 1, 1000000)),
				"include": Arr("topic numbers of people held because the screenshot says they have left, which the USER said to import after all",
					Int("a topic number", 1, 1000000)),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				ov := ImportOverrides{
					Columns: map[Column]string{},
					Values:  map[Column]map[string]string{},
					Rows:    map[int]map[Column]string{},
				}
				for _, m := range argMaps(a, "columns") {
					ov.Columns[Column(argStr(m, "field"))] = argStr(m, "header")
				}
				for _, m := range argMaps(a, "values") {
					f := Column(argStr(m, "field"))
					if ov.Values[f] == nil {
						ov.Values[f] = map[string]string{}
					}
					ov.Values[f][argStr(m, "from")] = argStr(m, "to")
				}
				for _, m := range argMaps(a, "rows") {
					n := argInt(m, "row")
					if ov.Rows[n] == nil {
						ov.Rows[n] = map[Column]string{}
					}
					ov.Rows[n][Column(argStr(m, "field"))] = argStr(m, "value")
				}
				for _, x := range argInts(a, "ignore") {
					ov.Ignore = append(ov.Ignore, x)
				}
				for _, x := range argInts(a, "include") {
					ov.Include = append(ov.Include, x)
				}
				sess, err := s.RestageImport(v, argStr(a, "import_id"), ov)
				if err != nil {
					return nil, err
				}
				sum, ok := s.ImportSummary(v, sess.ID)
				if !ok {
					return nil, ErrNotFound
				}
				return sum, nil
			},
		},
		Tool{
			// Irreversible on purpose. Relaying one merge decision is a
			// conversation; relaying two hundred rows' worth in one call is a
			// different act, and it gets the same gate as a deletion: a human
			// approved THESE arguments, including every resolution in them.
			Name: "import_commit", Risk: RiskIrreversible,
			Description: "Apply a staged import once the user has decided the rows that needed deciding. Requires a human approval of these exact arguments, resolutions included.",
			Schema: Obj("what to apply", map[string]*Schema{
				"import_id": Str("the staged import; omit for the most recent one"),
				"decisions": Arr("one entry per row that needed deciding", Obj("one decision", map[string]*Schema{
					"index":         Int("the row's index in the plan", 0, 100000),
					"choice":        Str("what they decided", string(ChooseCreateNew), string(ChooseMergeInto), string(ChooseAcceptChange), string(ChooseKeepCurrent)),
					"merge_into_id": Str("required when the choice is merge_into"),
				}, "index", "choice")),
			}),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				res := map[int]Resolution{}
				for _, m := range argMaps(a, "decisions") {
					res[argInt(m, "index")] = Resolution{
						Choice: Choice(argStr(m, "choice")), MergeIntoID: argStr(m, "merge_into_id"),
					}
				}
				return s.CommitImport(v, argStr(a, "import_id"), res, time.Now().UTC())
			},
		},
		Tool{
			// Adding a contact is additive and visible; REMOVING one is how a
			// team quietly loses its only way to reach somebody. So removal is
			// a person's decision, relayed, and it leaves a trail.
			Name: "contact_remove", Risk: RiskWrite,
			Description: "Remove one way of reaching somebody, when the USER says it is wrong or out of date. Adding a contact needs no tool - it travels with the fact. Removing one does, because it is the half that loses something.",
			Schema: Obj("which contact", map[string]*Schema{
				"node_id": Str("whose"),
				"kind":    Str("the channel", string(ContactPhone), string(ContactEmail), string(ContactWeChat), string(ContactOther)),
				"value":   Str("the value to remove"),
				"why":     Str("what the user said, e.g. 这个号码停机了"),
			}, "node_id", "kind", "value", "why"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				err := s.RemoveContact(v, ActorUser, argStr(a, "node_id"),
					ContactKind(argStr(a, "kind")), argStr(a, "value"),
					Intel{Kind: IntelUserSaid, Excerpt: argStr(a, "why")})
				if err != nil {
					return nil, err
				}
				n, _ := s.Node(v, argStr(a, "node_id"))
				return n, nil
			},
		},
		Tool{
			Name: "graph_forget", Risk: RiskIrreversible,
			Description: "Permanently delete one record and everything that names it. Requires a human approval of these exact arguments.",
			Schema: Obj("what to delete", map[string]*Schema{
				"node_id": Str("the record to delete"),
			}, "node_id"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				return map[string]int{"removed": s.Forget(v, argStr(a, "node_id"))}, nil
			},
		},
		Tool{
			Name: "subject_request", Risk: RiskIrreversible,
			Description: "Answer a person's request about their own data: return everything held about them across all teams, or erase it. Requires a human approval of these exact arguments.",
			Schema: Obj("whose data, and what to do", map[string]*Schema{
				"label":  Str("the person's name as recorded"),
				"org":    Str("their company, if known"),
				"action": Str("what they asked for", "access", "erase"),
			}, "label", "action"),
			Run: func(s *Store, v View, a map[string]any) (any, error) {
				m := SubjectMatch{Org: argStr(a, "org"), Label: argStr(a, "label")}
				if argStr(a, "action") == "erase" {
					return s.ForgetSubject(m, time.Now().UTC()), nil
				}
				return s.SubjectRecords(m, time.Now().UTC()), nil
			},
		},
	)
}
