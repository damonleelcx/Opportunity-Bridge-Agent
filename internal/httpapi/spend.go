package httpapi

// The spending gate: what an account may spend in a day, and what the whole
// deployment may spend in a day.
//
// Why it exists. Sign-up needs no invite code, so anybody who has the address
// can hold an account, and every turn spends model tokens against a paid key.
// agent.Budget bounds ONE turn; nothing bounded the number of turns. Two
// counters close that, and the second is not redundant with the first: accounts
// are free and unlimited, so a per-account allowance on its own multiplies by
// however many somebody registers.
// See docs/bugfix/2026-09-01-per-account-and-deployment-spend-caps.md

import (
	"fmt"
	"net/http"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// spendAllowed reports whether this request may start a turn, and writes the
// refusal itself when it may not.
//
// The check is "may this turn START", not a ceiling enforced mid-answer, so one
// turn can carry the total past the cap. That is deliberate: stopping a turn in
// flight would throw away the tokens it has already spent AND leave somebody
// looking at half a sentence. The overshoot is bounded by the per-turn ceilings
// that already exist (agent.Budget), which is precisely the job they do well.
func (s *Server) spendAllowed(w http.ResponseWriter, r *http.Request) bool {
	acct := accountFor(r)
	if acct == nil {
		// Unreachable through the router — the gate refuses this path without a
		// sign-in — but a nil account here must not panic a turn, and there is
		// nothing to charge without one.
		return true
	}
	accountSpent, deploymentSpent := s.Store.SpentToday(acct.Username)

	// The deployment ceiling is tested FIRST. When both are exceeded the person
	// needs the truer of the two sentences: "the service has stopped", not "you
	// have used your allowance", which would send them away believing they did
	// something wrong and that another account would fix it.
	if s.Cfg.DeploymentDailyTokens > 0 && deploymentSpent >= s.Cfg.DeploymentDailyTokens {
		s.Log.Warn("turn refused: the deployment's daily model budget is spent",
			"code", "SERVICE_BUDGET_REACHED", "username", acct.Username,
			"spent_tokens", deploymentSpent, "ceiling_tokens", s.Cfg.DeploymentDailyTokens)
		writeErr(w, http.StatusServiceUnavailable, "SERVICE_BUDGET_REACHED",
			"This service has used up what it is allowed to spend today, so it cannot answer anyone until "+resetsAt()+".",
			"This is not about your account and nothing is wrong with it. Come back after "+resetsAt()+
				", or tell whoever runs this service — the daily limit may simply be set too low.")
		return false
	}
	if s.Cfg.AccountDailyTokens > 0 && accountSpent >= s.Cfg.AccountDailyTokens {
		s.Log.Info("turn refused: account reached its daily allowance",
			"code", "SPEND_CAP_REACHED", "username", acct.Username,
			"spent_tokens", accountSpent, "cap_tokens", s.Cfg.AccountDailyTokens)
		writeErr(w, http.StatusTooManyRequests, "SPEND_CAP_REACHED",
			"Your account has used everything it is allowed to use today.",
			"Your conversations and your record are untouched and still here. The allowance refills at "+
				resetsAt()+"; come back then and carry on.")
		return false
	}
	return true
}

// recordSpend charges a finished turn to the account that ran it.
//
// Charged even when the turn ended in an error. A turn that failed on its last
// iteration still spent everything before it, and a cap that only counts
// successes is one that can be walked around with turns engineered to fail.
//
// A failed write logs and returns, which is the store's contract everywhere
// else: the turn has already been answered and refusing to acknowledge it would
// help nobody. The consequence is worth stating plainly — while the database is
// unwritable, spending is not counted and the caps do not bite. Refusing turns
// whenever the database is unhappy would break the main path in order to protect
// a budget, which is the wrong way round for this product.
func (s *Server) recordSpend(r *http.Request, u llm.Usage) {
	acct := accountFor(r)
	if acct == nil {
		return
	}
	// Input counts as well as output. Input is the larger half of a turn here —
	// history, tool definitions and retrieved corpus text — and it is billed,
	// so a cap that watched only output would be watching the smaller number.
	// Cache reads are deliberately NOT added: they are already inside the input
	// count reported for the turn, and adding them again would charge twice for
	// the cheapest tokens in the request.
	total := u.InputTokens + u.OutputTokens
	if total <= 0 {
		return
	}
	accountNow, deploymentNow := s.Store.AddSpend(acct.Username, total)

	// Each ceiling is announced ONCE — by the turn that crossed it — rather than
	// on every refusal that follows. `now-total < ceiling <= now` is what "this
	// turn is the one that crossed" means, and both numbers come from inside the
	// same lock, so two concurrent turns cannot both claim the crossing.
	//
	// A service-wide stop is an operator event: ERROR, because everybody is
	// refused until midnight and somebody needs to decide whether the ceiling is
	// simply set too low. One account reaching its allowance is ordinary and
	// expected: INFO.
	if crossed(deploymentNow, total, s.Cfg.DeploymentDailyTokens) {
		s.Log.Error("the deployment's daily model budget is now spent; every turn is refused until "+resetsAt(),
			"code", "SERVICE_BUDGET_REACHED", "spent_tokens", deploymentNow,
			"ceiling_tokens", s.Cfg.DeploymentDailyTokens)
	}
	if crossed(accountNow, total, s.Cfg.AccountDailyTokens) {
		s.Log.Info("account has used its daily allowance", "code", "SPEND_CAP_REACHED",
			"username", acct.Username, "spent_tokens", accountNow,
			"cap_tokens", s.Cfg.AccountDailyTokens)
	}
}

// crossed reports whether the turn that just added `by` is the one that took the
// total from under `ceiling` to at or over it. A ceiling of 0 is disabled and is
// never crossed.
func crossed(now, by, ceiling int64) bool {
	return ceiling > 0 && now >= ceiling && now-by < ceiling
}

// resetsAt names the moment the allowances refill, in the words somebody
// reading the refusal can act on.
//
// UTC is stated rather than hidden: the counters roll over on the UTC day (see
// store/spend.go), and a bare "tomorrow" is wrong for anybody whose tomorrow
// starts at a different hour than the counter's does.
func resetsAt() string {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	hours := int(next.Sub(now).Hours())
	if hours < 1 {
		return "00:00 UTC, less than an hour from now"
	}
	return fmt.Sprintf("00:00 UTC, about %d hours from now", hours)
}
