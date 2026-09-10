package leadgraph

import (
	"fmt"
	"sort"
	"strings"
)

// Sensitive personal information never enters this graph.
//
// TWO REASONS, ONE LEGAL ONE COMMERCIAL
//
//	《个人信息保护法》28-29: sensitive personal information needs separate,
//	specific consent and a necessity argument. This product has neither and
//	cannot get them - the people in this graph never met it.
//
//	And it is USELESS for the job. Nothing here gets a recruiter closer to a
//	placement. It is pure downside: it adds the category of breach that ends
//	a B2B contract, in exchange for nothing.
//
// WHY A REFUSAL AT THE WRITE AND NOT A REVIEW LATER
//
//	"We try not to record it" is a policy. A policy is what you have instead
//	of a mechanism. Once it is in the store it is in the backups, in the
//	exports, and in the answer to a subject access request.
//
// THE FALSE-POSITIVE TRADE, STATED PLAINLY
//
//	Every term here is one somebody could legitimately want to type - "残疾证
//	办理专员" is a real job. The list is therefore kept NARROW and SPECIFIC:
//	multi-character terms that are hard to mean innocently, never bare words
//	like 病 or 残疾. When it does misfire, the error says which term matched and
//	which category, so the user can rephrase in one go instead of guessing.
//	Widening this list is a product decision, not a bug fix.

// SensitiveCategory is the class of information a term belongs to. It exists so
// the refusal can name the rule rather than just the word.
type SensitiveCategory string

const (
	SensitiveHealth   SensitiveCategory = "health"   // 健康
	SensitiveFamily   SensitiveCategory = "family"   // 婚育
	SensitiveReligion SensitiveCategory = "religion" // 宗教
	SensitivePolitics SensitiveCategory = "politics" // 政治
	SensitiveFinance  SensitiveCategory = "finance"  // 金融账户与身份证件
	SensitiveLocation SensitiveCategory = "location" // 行踪轨迹
)

// sensitiveTerms is a table, not a chain of conditions: adding a category or a
// term is a row, and every caller gets it at once.
var sensitiveTerms = map[SensitiveCategory][]string{
	SensitiveHealth:   {"怀孕", "产假", "备孕", "抑郁症", "精神病", "癌症", "乙肝", "住院", "病假", "残疾证", "慢性病"},
	SensitiveFamily:   {"离婚", "已婚", "未婚", "二胎", "婚育", "配偶"},
	SensitiveReligion: {"基督徒", "佛教徒", "穆斯林", "信教", "宗教信仰"},
	SensitivePolitics: {"党员", "政治面貌", "入党", "党籍"},
	SensitiveFinance:  {"身份证号", "银行卡", "银行账号", "工资卡", "社保卡号", "征信"},
	SensitiveLocation: {"家庭住址", "居住地址", "行踪", "常驻地址", "定位记录"},
}

// SensitiveHit is what was found and where, so the message can be actionable.
type SensitiveHit struct {
	Field    string            `json:"field"`
	Term     string            `json:"term"`
	Category SensitiveCategory `json:"category"`
}

func (h SensitiveHit) Error() string {
	return fmt.Sprintf("%v: %s contains %q (%s). Sensitive personal information "+
		"cannot be stored here - remove it and record only what the role needs.",
		ErrSensitiveField, h.Field, h.Term, h.Category)
}

func (h SensitiveHit) Unwrap() error { return ErrSensitiveField }

// scanSensitive checks named fields and returns the first hit, deterministically:
// fields in the order given, categories and terms sorted, so the same input
// always names the same term. A refusal that cites a different word on each
// attempt is impossible to act on.
func scanSensitive(fields map[string]string) error {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)

	cats := make([]string, 0, len(sensitiveTerms))
	for c := range sensitiveTerms {
		cats = append(cats, string(c))
	}
	sort.Strings(cats)

	for _, name := range names {
		v := fields[name]
		if strings.TrimSpace(v) == "" {
			continue
		}
		for _, c := range cats {
			terms := append([]string{}, sensitiveTerms[SensitiveCategory(c)]...)
			sort.Strings(terms)
			for _, t := range terms {
				if strings.Contains(v, t) {
					return SensitiveHit{Field: name, Term: t, Category: SensitiveCategory(c)}
				}
			}
		}
	}
	return nil
}
