package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// 猎源图谱 (Lead Graph) as a capability of this agent.
//
// WHICH WAY THE DEPENDENCY POINTS, AND WHY IT MATTERS
//
//	This file is the adapter, and it lives HERE - in the agent's own tools
//	package - so the dependency runs agent → leadgraph and never the other way.
//	internal/leadgraph imports none of this product's packages, and a test in
//	that package asserts it. Pointing it the other way would put this product's
//	consent scopes and roles inside the thing they are meant to constrain, and
//	the two would start defining each other.
//
// WHY THIS DOES NOT BREAK THE FOUNDING SENTENCE
//
//	"It does not decide eligibility, and it does not score people." Lead Graph
//	scores SITUATIONS: a Lead's subject is an organisational unit and the type
//	has no person field at all. The people listed under one are in name order,
//	never ranked. no_candidate_scoring stays on this intent and stays true.
//
// WHY ONLY talent_sourcing
//
//	It is the one intent an employer reaches, and the only role that should ever
//	see a recruiter's own contact graph. Every tool below is additionally
//	restricted to RoleRecruiter, so an intent misroute cannot hand a resident
//	somebody's private book.

// leadGraphNames is what talent_sourcing's allowlist must contain. Exported so
// the intent registry and this file cannot drift apart - a name in one and not
// the other is caught by TestAllowedToolsExist.
func LeadGraphToolNames() []string {
	out := make([]string, 0, len(leadGraphExposed))
	for _, n := range leadGraphExposed {
		out = append(out, n)
	}
	return out
}

// leadGraphExposed is the subset of Lead Graph's surface this agent offers.
//
// Deliberately not all of it. Import needs a file the model cannot produce and
// a review screen this agent does not have; deletion and subject-rights work
// are irreversible and belong behind this product's own approval flow, which
// they are not wired to yet. Both are named in the PRD as deferred rather than
// quietly missing.
var leadGraphExposed = []string{
	"record_turn",     // the agent maintains the graph as the recruiter talks
	"graph_reconcile", // judge without writing
	"graph_query",     //
	"org_chart",       //
	"path_find",       // the reason any of this is worth having
	"lead_board",      //
	"stale_scan",      //
	"touchpoint_add",  //
	"rate_contact",    // without this path_find has nowhere to start
	"rating_gap",      //
	"answer_pending",  //
	"contact_remove",  //
	// Files arrive through POST /api/graph/imports, because the model cannot
	// produce one. What it CAN do is read the plan, propose a correction the
	// user confirms, and apply it - which is where an import actually fails.
	"import_summary",  //
	"import_remap",    //
	"import_commit",   // irreversible
	"graph_forget",    // irreversible
	"subject_request", // irreversible
}

var errNoGraph = errors.New("GRAPH_UNAVAILABLE: this deployment has no lead graph configured. " +
	"Set the graph database on the server; nothing here can be recorded until it is.")

// viewFor turns this session into a Lead Graph view.
//
// SeatID is the account, which is exactly right: private notes, relationship
// strengths and "only the seat that was asked may answer" are all about one
// person.
//
// TeamID is the account's ORG - the firm it was placed in by an operator.
//
// Never a self-declared string. An org somebody types is not an identity, it is
// a text field: type a competitor's name and you are inside their contact book.
// So it lives on the account, an operator sets it (`obagent team`), and this
// only reads it.
//
// An account with no org is a team of ONE, keyed by its own id. That is the
// right reading for a single consultant and the safe default for an account
// nobody has placed yet - it can see nothing but its own work, which is the
// failure direction to prefer.
func viewFor(env Env) (leadgraph.View, error) {
	if env.Graph == nil {
		return leadgraph.View{}, errNoGraph
	}
	if env.Session == nil || env.Session.SubjectID == "" {
		return leadgraph.View{}, errors.New("NO_SEAT: this request has no account to attribute the graph to")
	}
	id := env.Session.SubjectID
	return leadgraph.View{TeamID: GraphTeamFor(env.Store, id), SeatID: id}, nil
}

// GraphTeamFor is the ONE place the team rule lives.
//
// The upload route needs it too, and a second copy is how a route stages a file
// into one book while the conversation imports it into another. Exported for
// that reason and no other.
func GraphTeamFor(st *store.Store, subjectID string) string {
	if st != nil {
		if acct, ok := st.AccountBySubject(subjectID); ok && acct.Org != "" {
			return "org:" + strings.ToLower(acct.Org)
		}
	}
	return subjectID
}

// translateSchema converts a Lead Graph schema into this package's.
//
// A translation rather than a second hand-written copy: the schema is what the
// model is told AND what is enforced, so two copies would drift and the drift
// would show up as a tool that accepts something its implementation refuses.
func translateSchema(s *leadgraph.Schema) *Schema {
	if s == nil {
		return nil
	}
	out := &Schema{
		Type: s.Type, Description: s.Description, Required: s.Required,
		Enum: s.Enum, Minimum: s.Minimum, Maximum: s.Maximum,
		Items: translateSchema(s.Items),
	}
	if s.AdditionalProperties != nil {
		v := *s.AdditionalProperties
		out.AdditionalProperties = &v
	}
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]*Schema, len(s.Properties))
		for k, v := range s.Properties {
			out.Properties[k] = translateSchema(v)
		}
	}
	return out
}

func translateRisk(r leadgraph.Risk) Risk {
	switch r {
	case leadgraph.RiskIrreversible:
		return RiskIrreversible
	case leadgraph.RiskWrite:
		return RiskWrite
	default:
		return RiskRead
	}
}

// leadGraphTools wraps the exposed subset as this agent's tools.
func leadGraphTools() []Tool {
	reg := leadgraph.Tools()
	out := make([]Tool, 0, len(leadGraphExposed))
	for _, name := range leadGraphExposed {
		lt, ok := reg.Get(name)
		if !ok {
			// A name here with no tool behind it would be a tool the model is
			// offered and cannot call. Better to fail at construction than to
			// let the model discover it mid-conversation.
			panic("leadgraph tool missing: " + name)
		}
		out = append(out, Tool{
			Name:        lt.Name,
			Description: lt.Description,
			Schema:      translateSchema(lt.Schema),
			Risk:        translateRisk(lt.Risk),
			// An intent misroute must not hand a resident somebody's private
			// contact book. The allowlist is the first gate; this is the second.
			Roles: []domain.Role{domain.RoleRecruiter},
			Run: func(name string, lt leadgraph.Tool) func(context.Context, Env, map[string]any) (Result, error) {
				return func(_ context.Context, env Env, args map[string]any) (Result, error) {
					v, err := viewFor(env)
					if err != nil {
						return Result{}, err
					}
					if env.Graph.Degraded() {
						return Result{}, errors.New("GRAPH_DEGRADED: the graph stopped saving writes. " +
							"Nothing new can be recorded until the database is reachable again")
					}
					// Lead Graph asks for its own approval on an irreversible
					// call. Minting it HERE is safe and only here: Registry.Call
					// has already stopped this call once, shown a human the
					// exact arguments, and been given a decision on them. What
					// is relayed is that decision, not a bypass of it.
					var approvals []leadgraph.Approval
					if lt.Risk == leadgraph.RiskIrreversible {
						approvals = append(approvals, leadgraph.ApprovalFor(name, args))
					}
					got, err := leadgraph.Tools().Call(env.Graph, v, name, args, approvals)
					if err != nil {
						return Result{}, fmt.Errorf("%s: %w", name, err)
					}
					return Result{Content: got}, nil
				}
			}(lt.Name, lt),
		})
	}
	return out
}
