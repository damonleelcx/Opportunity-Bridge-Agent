package agent

import (
	"fmt"
	"strings"
	"time"
)

// This file holds the handful of sentences the SYSTEM writes directly to the
// person — not the model. They appear at the worst moments: a turn that ran out
// of budget, an answer that a guardrail stopped, a request the deployment has
// not switched on. Those are exactly the moments when being unreadable costs the
// most, so they are translated here rather than left in the code's language.
//
// A table, not a template engine: there are eight of them, they are safety
// messages, and they must be deterministic. If a locale is missing a key it
// falls back to English rather than to nothing.

type msgKey string

const (
	msgStopIterations msgKey = "stop.iterations"
	msgStopToolCalls  msgKey = "stop.tool_calls"
	msgStopOutput     msgKey = "stop.output"
	msgStopDeadline   msgKey = "stop.deadline"
	msgStopRepeated   msgKey = "stop.repeated"
	msgStopRefused    msgKey = "stop.refused"
	msgBlockHeader    msgKey = "block.header"
	msgBlockFooter    msgKey = "block.footer"
	msgAnswerEmpty    msgKey = "answer.empty"
	msgIntentDisabled msgKey = "intent.disabled"
	msgUnresolved     msgKey = "verify.unresolved"
)

var messages = map[string]map[msgKey]string{
	"en": {
		msgStopIterations: "I stopped after %d steps without finishing. What I found so far is above. " +
			"Ask me for one specific piece of it, or ask for a person to take over.",
		msgStopToolCalls: "I stopped after %d lookups without finishing. Narrowing the question — one city, " +
			"one kind of support — usually gets there. A person can also take this over.",
		msgStopOutput: "This answer grew too long to finish safely. Ask me for the single next step and I will give just that.",
		msgStopDeadline: "This took longer than the %s I am allowed and I stopped rather than leave you waiting. " +
			"Nothing was filed. Please ask again, or ask for a person.",
		msgStopRepeated: "I was repeating the same lookup without getting anywhere, so I stopped. " +
			"Something in the question and what I can search do not line up — tell me the city and what you are " +
			"trying to do, or ask for a person.",
		msgStopRefused: "I cannot answer this one. Nothing was done. If this is about a specific person's case, " +
			"a member of staff can help.",
		msgBlockHeader: "I stopped myself from sending that answer, because it broke a rule this service holds to:",
		msgBlockFooter: "Nothing was done and nothing was filed. Ask me again in narrower terms, " +
			"or ask for a person to take this over.",
		msgAnswerEmpty: "I could not produce an answer this time and nothing was done. " +
			"Please tell me the city and what you are trying to sort out, or ask for a person to take over.",
		msgIntentDisabled: "This kind of request (%s) is not switched on in this deployment yet. " +
			"Nothing was done. A member of staff can help in the meantime.",
		msgUnresolved: "\n\n(I checked this answer against my own rules and it still does not pass: %s. " +
			"It is the best I produced — ask me to say it again, or call 12333 and ask for a person.)",
	},
	"zh-CN": {
		msgStopIterations: "走了 %d 步还没办完，我先停下来了。上面是已经找到的部分。" +
			"你可以挑其中一件让我细说，也可以让我找个人来接手。",
		msgStopToolCalls: "查了 %d 次还没办完，我先停下来了。把问题收窄一点——一个城市、一类补助——" +
			"通常就能查到。也可以让人来接手。",
		msgStopOutput: "这条回答太长了，没法安全地写完。你问我下一步该做什么，我就只答那一件。",
		msgStopDeadline: "这次超过了我被允许的 %s，与其让你一直等，我先停了。什么都没有提交。" +
			"请再问一次，或者让我找个人来。",
		msgStopRepeated: "我在反复查同一件事，一直没查出结果，所以停下了。" +
			"你的问题和我能查到的东西对不上——告诉我城市和你想办的事，或者让我找个人来。",
		msgStopRefused: "这个我答不了，什么都没做。如果是某个具体的人的事，找工作人员会更快。",
		msgBlockHeader: "这条回答我拦下来了，没有发给你，因为它违反了本服务的一条硬规矩：",
		msgBlockFooter: "什么都没做，也什么都没提交。你可以把问题问得更具体一点，或者让我找个人来接手。",
		msgAnswerEmpty: "这次我没能给出答案，也什么都没做。" +
			"你告诉我城市和想办的事，或者让我找个人来接手。",
		msgIntentDisabled: "这类请求（%s）在这个部署里还没开通，什么都没做。这期间可以找工作人员帮忙。",
		msgUnresolved: "\n\n（这条回答我自己按规矩检查过，还是没完全通过：%s。这是我这次能写出的最好版本——" +
			"你可以让我重说一遍，或者直接打 12333 找人。）",
	},
}

// blockReasons are the rule violations a person can actually be shown — the
// Block-severity verifier findings, which end up quoted in the answer when a
// draft is stopped. The English Finding.Message stays as it is for the trace,
// the logs and the tests; this is the same fact said to the reader.
//
// A code with no entry falls back to the finding's own message, so a new
// verifier shows up in English rather than silently showing nothing.
var blockReasons = map[string]map[string]string{
	"zh-CN": {
		"INVENTED_IDENTIFIER":   "回答里出现了语料库中不存在的编号。有人可能拿着它跑一趟窗口。",
		"COHORT_DOWNRANKING":    "回答把你的身份当成了「别去试」的理由。身份只用来多给支持，不用来减少选择。",
		"CONSENT_MISSING":       "这一轮动了居民的记录，但档案里没有「让工作人员查看」的授权。",
		"SILENT_CLOSURE":        "有一条任务在没有任何凭据的情况下被标成了已完成。",
		"PII_IN_AGGREGATE":      "回答里出现了个人身份信息。这一类问题只谈群体，不谈具体的人。",
		"INTERNAL_ID_LEAKED":    "回答里出现了内部记录编号，可以被反查到具体的人。",
		"UNSOURCED_AGGREGATE":   "回答给了数字，但这些数字没有经过匿名下限的处理。",
		"NEXT_STEP_NOT_TRACKED": "这一轮找到了能办的事，但没有把它记进「进行中任务」，你下次回来就看不到了。",
		// 这两条挡的是同一件事：把程序内部的东西当成回答发给人看。
		// 见 docs/bugfix/2026-08-31-routing-json-shown-as-answer.md
		"ANSWER_IS_MACHINE_OUTPUT": "这一条回答是程序内部的数据，不是写给人看的话。",
		"ROUTING_OBJECT_LEAKED":    "回答里混进了系统内部的分流判断结果。那是给界面上那个标签用的，不该出现在正文里。",
	},
}

func blockReason(locale, code, fallback string) string {
	if strings.HasPrefix(locale, "zh") {
		if s, ok := blockReasons["zh-CN"][code]; ok {
			return s
		}
	}
	return fallback
}

// sysMsg renders one system-authored sentence in the person's language.
func sysMsg(locale string, key msgKey, args ...any) string {
	table := messages["en"]
	if strings.HasPrefix(locale, "zh") {
		if zh, ok := messages["zh-CN"]; ok {
			table = zh
		}
	}
	s, ok := table[key]
	if !ok {
		s = messages["en"][key]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Explain turns a stop reason into something worth showing a person: what
// happened, and what they can do about it. "iteration limit reached" is not a
// sentence anybody can act on.
func Explain(locale string, r StopReason, b *Budget) string {
	switch r {
	case StopMaxIterations:
		return sysMsg(locale, msgStopIterations, b.Iterations())
	case StopMaxToolCalls:
		return sysMsg(locale, msgStopToolCalls, b.ToolCalls())
	case StopMaxOutputTokens:
		return sysMsg(locale, msgStopOutput)
	case StopDeadline:
		return sysMsg(locale, msgStopDeadline, b.Allowance().Round(time.Second))
	case StopRepeatedToolCall:
		return sysMsg(locale, msgStopRepeated)
	case StopRefused:
		return sysMsg(locale, msgStopRefused)
	}
	return ""
}
