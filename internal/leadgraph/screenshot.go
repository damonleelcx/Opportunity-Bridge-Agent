package leadgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ---- importing a screenshot of a mind map ----
//
// WHY A SCREENSHOT GOES THROUGH A MODEL WHEN A SPREADSHEET DOES NOT
//
//	A spreadsheet can be read by code: the header says which column is which.
//	A screenshot is pixels, and the people in it are written however the
//	person who drew the map wrote them - "44 女 徐烨 穿戴设计负责人" and
//	"38 创新设计部 负责人 张彬" put the name in different places, and the
//	leading number is an age in one line and not in the next. No table of rules
//	reads that. So a model transcribes the picture and PROPOSES who each line
//	is, and everything after the transcription is this package's ordinary,
//	deterministic import: staged, shown row by row, corrected, approved.
//
//	The owner decided (2026-09-11): the picture is sent only after the person
//	uploading it confirms that it will be (checked server-side, per upload);
//	the model's names and titles are proposals a person checks row by row;
//	highlights are recorded verbatim, never read as "target" or "priority";
//	anyone the picture says has left is held until a person says to import them.
//	See docs/20-lead-graph.zh-CN.md §10.3.
//
// WHAT THIS FILE OWNS
//
//	The transcript contract - the prompt, the shape the model returns, and the
//	structural checks a reading must pass before anything is planned from it.
//	It calls no model: internal/vision does that, and hands the text here.

// TranscriptPrompt is the question a vision model answers about a screenshot.
//
// ‼️ The recorded readings in testdata/screenshot were produced with exactly this
// text, and the planning tests run against them. Change a word here and those
// readings no longer say what the model does: record new ones (see
// testdata/screenshot/README.md). TestTheRecordedReadingsCameFromThisPrompt holds
// the two together.
const TranscriptPrompt = `你是转写器。把这张思维导图截图转写成 json。

规则：
1. 每个可见节点一条记录，包括空的占位节点。不要漏，也不要编造看不到的节点。
2. text 必须逐字照抄节点里的文字：不改写、不补全、不翻译、不纠错，数字和标点原样保留。
3. parent 填父节点的 id；最左边的根节点 parent 为 null。
4. 标记照实填，看不出就填 null：
   - fill：整个节点的底色（"purple" / "yellow" / 其他颜色的英文名）
   - highlights：只有一部分文字带底色时，列出那几段文字和颜色 [{"text": "...", "color": "purple"}]
   - text_color：文字本身的颜色（不是黑色时才填），bold：文字是否加粗
   - line_color：连到这个节点的那条线的颜色（不是蓝色时才填）
   - placeholder：是否是「输入文本」这类空占位
5. 另外给出你的判断（只是建议，会由人核对）：
   - kind："org"（公司）/ "person"（人）/ "attribute"（不是人，只是附注，比如品牌、项目）/ "placeholder" / "root"
   - name：如果是人，写出姓名，照抄原文里的写法；看不出姓名填 null
   - title：如果是人，写出职务，照抄原文；没有填 null
   - departed：原文明确写了已经离开这家公司（比如「已离职」「离开」）才填 true

只输出 json：{"nodes": [{"id": 1, "parent": null, "text": "...", "fill": null, "highlights": [], "text_color": null, "bold": false, "line_color": null, "placeholder": false, "kind": "...", "name": null, "title": null, "departed": false}]}`

// TopicKind is what the model proposed a topic is.
type TopicKind string

const (
	TopicRoot        TopicKind = "root"
	TopicOrg         TopicKind = "org"
	TopicPerson      TopicKind = "person"
	TopicAttribute   TopicKind = "attribute"
	TopicPlaceholder TopicKind = "placeholder"
)

// TranscriptMark is a stretch of a topic's text with its own background.
type TranscriptMark struct {
	Text  string `json:"text"`
	Color string `json:"color"`
}

// TranscriptNode is one topic as the model read it. Text and the marks are a
// transcription; Kind, Name, Title and Departed are proposals.
type TranscriptNode struct {
	ID          int              `json:"id"`
	Parent      *int             `json:"parent"`
	Text        string           `json:"text"`
	Fill        string           `json:"fill,omitempty"`
	Highlights  []TranscriptMark `json:"highlights,omitempty"`
	TextColor   string           `json:"text_color,omitempty"`
	Bold        bool             `json:"bold,omitempty"`
	LineColor   string           `json:"line_color,omitempty"`
	Placeholder bool             `json:"placeholder,omitempty"`
	Kind        TopicKind        `json:"kind"`
	Name        string           `json:"name,omitempty"`
	Title       string           `json:"title,omitempty"`
	Departed    bool             `json:"departed,omitempty"`
}

// Transcript is one screenshot as the model read it.
type Transcript struct {
	Nodes []TranscriptNode `json:"nodes"`
}

// Bounds on a reading. A mind map a person screenshots has tens of topics; these
// exist so that a runaway reading is refused with a reason rather than planned.
const (
	MaxTranscriptNodes = 400
	MaxTopicRunes      = 400
	// TranscriptMaxTokens is the output ceiling for one reading. The 31-topic
	// synthetic map took 2.8-3.8k output tokens, so this leaves room for a map
	// four times its size; past that the reading is refused as truncated and
	// the person is told to crop, rather than a partial map being planned.
	TranscriptMaxTokens int64 = 16000
)

// ErrTranscriptInvalid means the model's reading cannot be planned from.
var ErrTranscriptInvalid = errors.New("TRANSCRIPT_INVALID")

// ParseTranscript reads the model's text and checks its structure.
//
// Structure only. Whether a topic is really a person is the model's proposal
// and a person's to correct; whether the reading hangs together as a tree is
// not a matter of opinion, and a reading that does not is refused whole rather
// than planned in part - a broken parent reference is exactly the error that
// would put somebody under the wrong manager.
//
// Several top-level topics are allowed. In the owner's own screenshot the root
// is scrolled off the left edge, so the companies ARE the top of what can be
// seen; demanding a single root would refuse the picture the feature is for.
func ParseTranscript(text string) (Transcript, error) {
	body := strings.TrimSpace(text)
	// A model asked for JSON sometimes fences it anyway.
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(strings.TrimSpace(body), "```")

	var t Transcript
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return Transcript{}, fmt.Errorf("%w: the reading is not the JSON that was asked for (%v). Upload the "+
			"screenshot again; if it keeps failing, crop it into smaller parts", ErrTranscriptInvalid, err)
	}
	if err := t.validate(); err != nil {
		return Transcript{}, err
	}
	return t, nil
}

func (t Transcript) validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format+". Upload the screenshot again; if it keeps failing, crop it into "+
			"smaller parts", append([]any{ErrTranscriptInvalid}, args...)...)
	}
	if len(t.Nodes) == 0 {
		return invalid("the reading found no topics in the picture")
	}
	if len(t.Nodes) > MaxTranscriptNodes {
		return invalid("the reading has %d topics; at most %d are planned from one picture", len(t.Nodes), MaxTranscriptNodes)
	}
	byID := make(map[int]TranscriptNode, len(t.Nodes))
	for _, n := range t.Nodes {
		if _, dup := byID[n.ID]; dup {
			return invalid("topic id %d appears twice", n.ID)
		}
		byID[n.ID] = n
	}
	top := 0
	for _, n := range t.Nodes {
		if n.Parent == nil {
			top++
		} else if *n.Parent == n.ID {
			return invalid("topic %d is its own parent", n.ID)
		} else if _, ok := byID[*n.Parent]; !ok {
			return invalid("topic %d hangs under topic %d, which the reading does not contain", n.ID, *n.Parent)
		}
		if strings.TrimSpace(n.Text) == "" && !n.Placeholder {
			return invalid("topic %d has no text and is not marked as a placeholder", n.ID)
		}
		if r := len([]rune(n.Text)); r > MaxTopicRunes {
			return invalid("topic %d is %d characters long; a topic in a mind map is at most %d", n.ID, r, MaxTopicRunes)
		}
	}
	if top == 0 {
		return invalid("every topic has a parent, so the reading is a loop with no top")
	}
	// Walk up from every topic. A chain longer than the number of topics has
	// visited one of them twice.
	for _, n := range t.Nodes {
		cur, steps := n, 0
		for cur.Parent != nil {
			if steps++; steps > len(t.Nodes) {
				return invalid("topic %d is part of a loop of parents", n.ID)
			}
			cur = byID[*cur.Parent]
		}
	}
	return nil
}
