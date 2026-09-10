package tools

// 猎源图谱's scheduled pass, and what it hands back to 阿桥 to say out loud.
//
// WHY THIS LIVES HERE AND NOT IN leadgraph
//
//	leadgraph knows about teams and seats. It does not know what an account is,
//	and it must not: this package is the one place that turns 阿桥's idea of a
//	person into that package's idea of a seat (GraphTeamFor). A scheduler in
//	leadgraph would need a second copy of that rule, and two copies is how a
//	pass runs against one book while the conversation writes another.
//
// WHY THE PASS EXISTS AT ALL
//
//	离职提醒 was a pull: the recruiter had to ask. RunDaily had no caller
//	anywhere in the tree, so nothing ever raised an alert, and nothing ever came
//	and found anybody. The user's words were "最好会有个离职提醒" — a reminder is
//	something that arrives.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// GraphColdAfter is how long without contact counts as cold on the daily pass.
const GraphColdAfter = 45 * 24 * time.Hour

// GraphDailyInterval is how often the pass runs.
//
// Daily, not hourly: what it looks for — a reorganisation, a group that has
// gone quiet — moves on the scale of weeks. A pass that ran hourly would cost
// twenty-four times as much to notice the same thing a day later.
const GraphDailyInterval = 24 * time.Hour

// GraphViews is every seat the pass should run as.
//
// One per account, because in this deployment a seat IS an account: the seat is
// the subject, and the team comes from the firm that subject belongs to. Both
// halves come from GraphTeamFor so this cannot drift from the tools.
func GraphViews(st *store.Store) []leadgraph.View {
	if st == nil {
		return nil
	}
	var out []leadgraph.View
	for _, acct := range st.AllAccounts() {
		if acct.SubjectID == "" {
			continue
		}
		out = append(out, leadgraph.View{
			TeamID: GraphTeamFor(st, acct.SubjectID), SeatID: acct.SubjectID,
		})
	}
	return out
}

// RunGraphDaily runs one pass for every seat and reports what it did.
//
// It takes no sources: this deployment has none configured, and FetchFrom is
// refused for the platforms that would be the obvious ones (see leadgraph's
// blocked host list). The pass is still worth running without them, because the
// work that actually matters here is reconciliation of what the RECRUITER said
// — every organisational change they mentioned should carry an alert, and every
// person nobody has contacted in a long time should be findable.
//
// A failure for one seat does not stop the others: one unreadable book is not a
// reason for nobody to get their alerts.
func RunGraphDaily(ctx context.Context, st *store.Store, graph *leadgraph.Store, log *slog.Logger, now time.Time) (seats, alerts, stale int) {
	if graph == nil {
		return 0, 0, 0
	}
	for _, v := range GraphViews(st) {
		rep, err := graph.RunDaily(ctx, v, nil, leadgraph.FetchRequest{}, now, GraphColdAfter)
		if err != nil {
			if log != nil {
				log.Warn("猎源图谱 daily pass failed for one seat",
					"code", "GRAPH_DAILY_SEAT_FAILED", "seat", v.SeatID, "error", err)
			}
			continue
		}
		seats++
		alerts += len(rep.Alerts)
		stale += len(rep.Stale)
	}
	if log != nil {
		log.Info("猎源图谱 daily pass", "code", "GRAPH_DAILY_DONE",
			"seats", seats, "alerts_raised", alerts, "gone_quiet", stale)
	}
	return seats, alerts, stale
}

// GraphNewsCap is how many pieces of news reach one turn's context.
//
// Three, for the same reason a receipt names at most three things: this arrives
// on top of whatever the person actually asked about, and a list they learn to
// scroll past is a list that has stopped working.
const GraphNewsCap = 3

// GraphNews is what 阿桥 has not yet told this seat about, and marks it told.
//
// WHY IT ACKNOWLEDGES AS IT READS
//
//	An alert is raised at most once ever, and this is the moment it reaches a
//	person. Leaving it unacknowledged would put the same sentence at the top of
//	every turn until they act on it, which is how a notification becomes
//	something people turn off. If the model declines to mention it, the alert is
//	still on the graph screen and in the lead board — it is not lost, it is just
//	no longer interrupting.
//
// The strings are English because everything the model reads is: the answer
// language is set separately, by the language directive.
func GraphNews(st *store.Store, graph *leadgraph.Store, subjectID string, now time.Time) []string {
	if graph == nil || subjectID == "" {
		return nil
	}
	v := leadgraph.View{TeamID: GraphTeamFor(st, subjectID), SeatID: subjectID}
	var out []string
	for _, a := range graph.Alerts(v, false) {
		if len(out) == GraphNewsCap {
			break
		}
		line := fmt.Sprintf("%s — %s", a.Org, a.EventLabel)
		if n := len(a.People); n > 0 {
			line += fmt.Sprintf("; %d people you have recorded are in that group", n)
		}
		line += fmt.Sprintf(" (%s)", a.Corroboration)
		out = append(out, line)
		graph.AckAlert(v, a.ID)
	}
	return out
}
