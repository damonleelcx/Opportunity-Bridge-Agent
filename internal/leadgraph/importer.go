package leadgraph

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Bulk import: the only thing that fills this graph on day one.
//
// WHY THIS EXISTS AT ALL
//
//	Every derived feature here needs data the user has not given us yet.
//	path_find returns NoSeeds until somebody has rated a contact; HiringGap
//	returns nil until the ledger has months of history; an alert needs both an
//	event and people you already know in that group. On an empty graph the
//	product knows nothing and can say nothing - and a consultant already has
//	the answer sitting in a spreadsheet.
//
// THE FORMAT RULE THAT DECIDES WHETHER ANYBODY USES IT
//
//	We adapt to THEIR file. A template they have to rename columns into is
//	"clean your data first, then use the product", and nobody does that. So
//	headers are matched through an alias table, unmapped columns are REPORTED
//	rather than silently dropped, and the two things we genuinely cannot work
//	without are refused loudly with the columns we did see.
//
// AND THE RULE THAT KEEPS IT HONEST
//
//	An import goes through Reconcile like everything else. It cannot create a
//	duplicate the conversation path would have asked about, and it writes
//	nothing until somebody has looked at the plan.

// Column is a field we know how to use. Canonical names, matched from whatever
// the file happens to call them.
type Column string

const (
	ColLabel    Column = "label"
	ColOrg      Column = "org"
	ColUnit     Column = "unit"
	ColRole     Column = "role_title"
	ColDuty     Column = "duty"
	ColStrength Column = "strength"
	ColNote     Column = "note"
	ColPhone    Column = "phone"
	ColEmail    Column = "email"
	ColWeChat   Column = "wechat"
)

// contactColumns maps the contact-bearing columns to what they produce. A table
// rather than three branches, so adding a channel is a row.
var contactColumns = map[Column]ContactKind{
	ColPhone:  ContactPhone,
	ColEmail:  ContactEmail,
	ColWeChat: ContactWeChat,
}

// columnAliases is a table, not a chain of conditions: a new spelling is a new
// entry and every import gets it at once. Lower-cased and space-stripped before
// comparison.
var columnAliases = map[Column][]string{
	ColLabel:    {"姓名", "名字", "人名", "联系人", "候选人", "name", "fullname", "full name", "contact"},
	ColOrg:      {"公司", "单位", "所在公司", "企业", "雇主", "现公司", "company", "organization", "org", "employer"},
	ColUnit:     {"部门", "团队", "业务组", "事业部", "组", "所在部门", "department", "team", "division", "group", "unit"},
	ColRole:     {"职务", "职位", "岗位", "title", "position", "role", "job title"},
	ColDuty:     {"职责", "负责", "分管", "工作内容", "responsibility", "scope", "duties"},
	ColStrength: {"熟悉程度", "关系强度", "亲密度", "熟悉度", "关系", "strength", "closeness", "rapport"},
	ColNote:     {"备注", "说明", "补充", "note", "notes", "comment", "remark"},
	ColPhone:    {"手机", "电话", "手机号", "联系电话", "联系方式", "phone", "mobile", "tel", "telephone", "cell"},
	ColEmail:    {"邮箱", "邮件", "电子邮箱", "email", "mail", "e-mail"},
	ColWeChat:   {"微信", "微信号", "wechat", "weixin", "wx"},
}

// SkippedRow is one thing the import did not take, with the row number. Silence
// here would be the worst outcome: the user believes 200 rows landed and 37 did
// not, and nothing on screen says which.
type SkippedRow struct {
	Row    int    `json:"row"`
	Column string `json:"column,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Skip reasons.
const (
	SkipNoName    = "no_name"
	SkipSensitive = "sensitive_field"
	SkipBadValue  = "bad_value"
)

// ImportOverrides is the user's correction to how their file was read.
//
// WHY REPAIR IS A RE-READ AND NOT AN EDIT
//
//	Every fix here changes how the FILE is interpreted, and the whole file is
//	then read again from the original bytes. Nothing is patched in place, so
//	there is no half-corrected plan, and the user can always see the current
//	reading in one piece rather than as a base plus a pile of amendments.
//
// WHY THE AGENT PROPOSES AND THE USER CONFIRMS
//
//	Guessing that column 3 is the company and then importing 200 rows on that
//	guess is not a small error - it is 200 wrong records that all look right.
//	The agent can see the header and a few sample rows and say what it thinks;
//	turning that into an override is the user agreeing.
type ImportOverrides struct {
	// Columns maps a canonical field to a header name, or to "#3" for the third
	// column when the header is blank or duplicated.
	Columns map[Column]string `json:"columns,omitempty"`
	// Values normalises what a column actually contains: 很熟 -> 3, 一般 -> 2.
	// A file uses the vocabulary its owner uses, and that is not a defect.
	Values map[Column]map[string]string `json:"values,omitempty"`
	// Rows fills in a single cell the user supplied, keyed by the line number
	// as reported. Used for the name a row was missing - NEVER invented, only
	// relayed.
	Rows map[int]map[Column]string `json:"rows,omitempty"`
	// Ignore are line numbers the user said to leave out.
	Ignore []int `json:"ignore,omitempty"`
}

// Why a plan can be unusable as it stands.
const (
	BlockNoNameColumn = "no_name_column"
	BlockNoRows       = "no_rows"
)

// MaxImportBytes caps a staged file. Generous for a contact list, and a runaway
// upload is refused with a reason rather than sitting in memory.
const MaxImportBytes = 8 << 20

// SampleRows is how much of the file to keep for inspection. Enough for
// somebody - or an agent - to tell a column of companies from a column of job
// titles, and not enough to be a second copy of the file.
const SampleRows = 5

// ImportPlan is what WOULD happen. Nothing is written to produce it.
type ImportPlan struct {
	File      string `json:"file"`
	Digest    string `json:"digest"`
	Encoding  string `json:"encoding"`
	Delimiter string `json:"delimiter"`
	Rows      int    `json:"rows"`
	// Mapping is canonical field -> the header we matched it to, so the user can
	// check we read their file the way they meant it.
	Mapping map[string]string `json:"mapping"`
	// Unmapped are headers we did not use. Reported so a column of phone
	// numbers does not quietly vanish and get called an import.
	Unmapped  []string     `json:"unmapped"`
	Proposals []Proposal   `json:"proposals"`
	Skipped   []SkippedRow `json:"skipped"`
	// Strengths and Notes map a proposal index to the private values the file
	// carried. They travel BESIDE the proposals rather than inside them,
	// because Reconcile strips a note from a candidate on purpose - a private
	// impression is not part of who somebody is, and identity is what a
	// proposal is about. Carried in the candidate they would simply vanish,
	// which is exactly what happened until a test caught it.
	//
	// Both are applied only after the records exist, through Annotate, so they
	// land in this seat's own overlay and nowhere else.
	// Blocked, when set, means the file could not be read usefully as it
	// stands. It is NOT an error: the file is staged anyway so that somebody can
	// look at it and say how to read it. Refusing outright would send the user
	// back to their spreadsheet, which is the thing this feature exists to
	// avoid.
	Blocked string `json:"blocked,omitempty"`
	// Header and Samples are the file as it arrived, so a reader can work out
	// which column is which.
	Header  []string   `json:"header,omitempty"`
	Samples [][]string `json:"samples,omitempty"`
	// Overrides is how this reading was corrected, carried so the current
	// reading is always visible in one piece.
	Overrides ImportOverrides `json:"overrides,omitempty"`
	Strengths map[int]int     `json:"strengths,omitempty"`
	Notes     map[int]string  `json:"notes,omitempty"`
	// RowOf maps a proposal back to the line it came from, so a question can be
	// asked as "第 42 行的王总" and the user can look at their own file.
	RowOf map[int]int `json:"row_of,omitempty"`
}

// Counts summarises the plan for the confirmation screen. Computed from the
// proposals themselves so the number and the list cannot disagree.
func (p ImportPlan) Counts() map[string]int {
	c := map[string]int{"create": 0, "update": 0, "needs_you": 0, "skipped": len(p.Skipped)}
	for _, x := range p.Proposals {
		switch x.Decision {
		case DecideCreate:
			c["create"]++
		case DecideUpdate:
			c["update"]++
		default:
			c["needs_you"]++
		}
	}
	return c
}

// ImportResult is what actually happened.
type ImportResult struct {
	Created       []string `json:"created,omitempty"`
	Updated       []string `json:"updated,omitempty"`
	Pending       []int    `json:"pending,omitempty"`
	Rated         int      `json:"rated"`
	Noted         int      `json:"noted"`
	ObservationID string   `json:"observation_id"`
}

var (
	ErrNoHeader = fmt.Errorf("%w: the first row must be a header", ErrBadArguments)
	// ErrNoNameColumn names what we DID see, because "we could not find a name
	// column" with no list is a dead end for the person holding the file.
	ErrNoNameColumn = fmt.Errorf("%w: no column looks like a person's name", ErrBadArguments)
)

// ---- reading the file ----

type table struct {
	header    []string
	rows      [][]string
	encoding  string
	delimiter string
}

// decode handles the encoding trap that produces most "import worked, output is
// 乱码" reports: a CSV exported from a Chinese Excel is GBK, not UTF-8.
func decode(raw []byte) ([]byte, string) {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return raw[3:], "utf-8-bom"
	}
	if utf8.Valid(raw) {
		return raw, "utf-8"
	}
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
	if err != nil {
		return raw, "unknown"
	}
	return out, "gbk"
}

// sniffDelimiter picks whichever separator appears most in the header line.
// Wrong guesses are visible immediately - the mapping comes back empty and the
// header shows as one long column - which is better than a silent misparse.
func sniffDelimiter(first string) rune {
	best, bestN := ',', strings.Count(first, ",")
	for _, c := range []rune{'\t', ';'} {
		if n := strings.Count(first, string(c)); n > bestN {
			best, bestN = c, n
		}
	}
	return best
}

func readTable(raw []byte) (*table, error) {
	body, enc := decode(raw)
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	first := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	}
	d := sniffDelimiter(first)

	r := csv.NewReader(strings.NewReader(text))
	r.Comma = d
	r.FieldsPerRecord = -1 // ragged rows are normal in hand-kept spreadsheets
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadArguments, err)
	}
	if len(recs) < 2 {
		return nil, ErrNoHeader
	}
	return &table{header: recs[0], rows: recs[1:], encoding: enc, delimiter: string(d)}, nil
}

func normHeader(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "_", "", "-", "", "　", "").Replace(s)
	return s
}

// mapColumns matches headers to canonical fields. First match wins per column,
// and each canonical field is claimed once - a file with both 「公司」 and
// 「现公司」 uses the first, and the second is reported as unmapped rather than
// silently overwriting.
func mapColumns(header []string) (map[Column]int, []string) {
	out := map[Column]int{}
	used := map[int]bool{}
	fields := make([]Column, 0, len(columnAliases))
	for c := range columnAliases {
		fields = append(fields, c)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i] < fields[j] })

	for _, field := range fields {
		for i, h := range header {
			if used[i] {
				continue
			}
			n := normHeader(h)
			for _, alias := range columnAliases[field] {
				if n == normHeader(alias) {
					out[field] = i
					used[i] = true
					break
				}
			}
			if _, done := out[field]; done {
				break
			}
		}
	}
	var unmapped []string
	for i, h := range header {
		if !used[i] && strings.TrimSpace(h) != "" {
			unmapped = append(unmapped, h)
		}
	}
	return out, unmapped
}

func cell(row []string, idx int, ok bool) string {
	if !ok || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

// ---- planning ----

// PlanImport reads a file and works out what it would do. It writes nothing.
func (s *Store) PlanImport(v View, fileName string, raw []byte, ov ImportOverrides) (ImportPlan, error) {
	if !v.valid() {
		return ImportPlan{}, ErrViewRequired
	}
	if len(raw) > MaxImportBytes {
		return ImportPlan{}, fmt.Errorf("%w: the file is %d bytes; the limit is %d",
			ErrBadArguments, len(raw), MaxImportBytes)
	}
	t, err := readTable(raw)
	if err != nil {
		return ImportPlan{}, err
	}
	cols, unmapped := mapColumns(t.header)
	applyColumnOverrides(cols, t.header, ov.Columns)
	unmapped = unusedHeaders(t.header, cols)

	plan := ImportPlan{
		File: fileName, Digest: digestOf(string(raw)), Encoding: t.encoding,
		Delimiter: t.delimiter, Rows: len(t.rows),
		Mapping: map[string]string{}, Unmapped: unmapped,
		Skipped: []SkippedRow{}, Strengths: map[int]int{}, Notes: map[int]string{},
		RowOf: map[int]int{}, Header: t.header, Overrides: ov,
	}
	for i, row := range t.rows {
		if i >= SampleRows {
			break
		}
		plan.Samples = append(plan.Samples, row)
	}
	for c, i := range cols {
		plan.Mapping[string(c)] = t.header[i]
	}
	// A file we cannot read is STAGED, not refused: somebody has to be able to
	// look at it and say which column is the name.
	if _, ok := cols[ColLabel]; !ok {
		plan.Blocked = BlockNoNameColumn
		return plan, nil
	}
	if len(t.rows) == 0 {
		plan.Blocked = BlockNoRows
		return plan, nil
	}

	ignored := map[int]bool{}
	for _, n := range ov.Ignore {
		ignored[n] = true
	}

	// One source for the whole file, not one per row: two rows naming the same
	// person are one file saying it twice, and counting them as independent
	// would promote it to corroborated on its own.
	fileRef := "import:" + plan.Digest

	var candidates []Node
	var strengths []int
	var notes []string
	var lines []int
	for n, row := range t.rows {
		lineNo := n + 2 // header is line 1
		if ignored[lineNo] {
			continue
		}
		fixed := ov.Rows[lineNo]
		pick := func(c Column) string {
			if v, ok := fixed[c]; ok {
				return strings.TrimSpace(v)
			}
			idx, has := cols[c]
			return mapValue(ov.Values[c], cell(row, idx, has))
		}
		label := pick(ColLabel)
		if label == "" {
			plan.Skipped = append(plan.Skipped, SkippedRow{Row: lineNo, Column: plan.Mapping["label"], Reason: SkipNoName})
			continue
		}
		c := Node{
			Kind: KindPerson, Label: label,
			Org:       pick(ColOrg),
			RoleTitle: pick(ColRole),
			Duty:      pick(ColDuty),
			Note:      pick(ColNote),
		}
		if u := pick(ColUnit); u != "" && c.Org != "" {
			c.UnitPath = []string{c.Org, u}
		}
		for _, col := range []Column{ColPhone, ColEmail, ColWeChat} {
			if val := pick(col); val != "" {
				c.Contacts = append(c.Contacts, ContactPoint{
					Kind: contactColumns[col], Value: val, Source: fileRef,
				})
			}
		}

		// A field carrying sensitive information is dropped; the row is not, and
		// the file certainly is not. Refusing the whole import over one cell
		// would mean the user deletes a column blindly and tries again.
		kept := c.Contacts[:0]
		for _, cp := range c.Contacts {
			if err := scanSensitive(map[string]string{string(cp.Kind): cp.Value}); err != nil {
				var hit SensitiveHit
				if asSensitive(err, &hit) {
					plan.Skipped = append(plan.Skipped, SkippedRow{
						Row: lineNo, Column: plan.Mapping[hit.Field], Reason: SkipSensitive,
						Detail: hit.Term + " (" + string(hit.Category) + ")",
					})
				}
				continue
			}
			kept = append(kept, cp)
		}
		c.Contacts = kept

		for name, ptr := range map[string]*string{
			"label": &c.Label, "role_title": &c.RoleTitle, "duty": &c.Duty, "note": &c.Note,
		} {
			if err := scanSensitive(map[string]string{name: *ptr}); err != nil {
				var hit SensitiveHit
				if ok := asSensitive(err, &hit); ok {
					plan.Skipped = append(plan.Skipped, SkippedRow{
						Row: lineNo, Column: plan.Mapping[name], Reason: SkipSensitive,
						Detail: hit.Term + " (" + string(hit.Category) + ")",
					})
				}
				*ptr = ""
			}
		}
		if c.Label == "" {
			plan.Skipped = append(plan.Skipped, SkippedRow{Row: lineNo, Reason: SkipNoName})
			continue
		}

		c.Intel = []Intel{{
			Kind: IntelImported, TurnRef: fileRef,
			Excerpt: fmt.Sprintf("%s 第 %d 行：%s", fileName, lineNo, strings.Join(compact(row), " / ")),
		}}
		lines = append(lines, lineNo)
		notes = append(notes, c.Note)
		c.Note = "" // travels beside the proposal; see ImportPlan.Notes
		candidates = append(candidates, c)

		st := 0
		if _, ok := cols[ColStrength]; ok || fixed[ColStrength] != "" {
			if raw := pick(ColStrength); raw != "" {
				n, err := strconv.Atoi(raw)
				switch {
				case err != nil || n < 1 || n > 3:
					plan.Skipped = append(plan.Skipped, SkippedRow{
						Row: lineNo, Column: plan.Mapping["strength"], Reason: SkipBadValue,
						Detail: raw + " (want 1-3)",
					})
				default:
					st = n
				}
			}
		}
		strengths = append(strengths, st)
	}

	plan.Proposals = s.Reconcile(v, candidates)
	for i := range plan.Proposals {
		if i < len(strengths) && strengths[i] > 0 {
			plan.Strengths[i] = strengths[i]
		}
		if i < len(notes) && notes[i] != "" {
			plan.Notes[i] = notes[i]
		}
		if i < len(lines) {
			plan.RowOf[i] = lines[i]
		}
	}
	return plan, nil
}

func compact(row []string) []string {
	out := make([]string, 0, len(row))
	for _, c := range row {
		if s := strings.TrimSpace(c); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func asSensitive(err error, out *SensitiveHit) bool {
	h, ok := err.(SensitiveHit)
	if ok {
		*out = h
	}
	return ok
}

// ApplyImport writes the plan, applies the relationship strengths the file
// carried, files the import in the ledger and records that it happened.
//
// The strengths are the point of importing at all for path_find: with none
// recorded there is nowhere for a route to start, so the one moment a
// consultant is willing to say who they actually know is while they are
// importing the file that lists them.
func (s *Store) ApplyImport(v View, plan ImportPlan, res map[int]Resolution, at time.Time) (ImportResult, error) {
	if !v.valid() {
		return ImportResult{}, ErrViewRequired
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	applied, err := s.Apply(v, ActorUser, plan.Proposals, res)
	if err != nil {
		return ImportResult{}, err
	}
	out := ImportResult{Created: applied.Created, Updated: applied.Updated, Pending: applied.Pending}

	byLabel := map[string]string{}
	for _, n := range s.Nodes(v, NodeFilter{Kind: KindPerson}) {
		byLabel[norm(n.Org)+"|"+norm(n.Label)] = n.ID
	}
	private := map[int]bool{}
	for i := range plan.Strengths {
		private[i] = true
	}
	for i := range plan.Notes {
		private[i] = true
	}
	idxs := make([]int, 0, len(private))
	for i := range private {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		if i >= len(plan.Proposals) {
			continue
		}
		c := plan.Proposals[i].Candidate
		id, ok := byLabel[norm(c.Org)+"|"+norm(c.Label)]
		if !ok {
			// The row needed a decision nobody made, so there is no record to
			// annotate. Its strength and note wait with it rather than being
			// dropped: re-applying the plan after answering picks them up.
			continue
		}
		if err := s.Annotate(v, ActorUser, id, plan.Strengths[i], plan.Notes[i]); err == nil {
			if plan.Strengths[i] > 0 {
				out.Rated++
			}
			if plan.Notes[i] != "" {
				out.Noted++
			}
		}
	}

	obs := s.record(v, Document{
		URL: "import:" + plan.Digest, Kind: DocImportedFile, Title: plan.File,
		Text: plan.File + " · " + itoa(plan.Rows) + " rows", FetchedAt: at,
	}, append(append([]string{}, out.Created...), out.Updated...))
	out.ObservationID = obs.ID

	s.audit(v, AuditImport, map[string]string{
		"file": plan.File, "digest": plan.Digest, "rows": itoa(plan.Rows),
		"created": itoa(len(out.Created)), "updated": itoa(len(out.Updated)),
		"needs_you": itoa(len(out.Pending)), "skipped": itoa(len(plan.Skipped)),
	}, at)
	return out, nil
}

// ---- staging: the review that is NOT the conversation queue ----
//
// WHY IMPORT DECISIONS DO NOT GO INTO s.pending
//
//	The conversation queue exists because the user is in the middle of doing
//	something else, which is why a turn asks at most one question. An import is
//	the opposite situation: they uploaded a file and are looking at the screen.
//	Twelve merge decisions belong on one review screen, answered together.
//
//	Putting them in the same queue would break both: the conversation gets
//	buried under twelve questions it was designed never to ask, or the import
//	takes twelve turns. Separate structures, because they are separate moments.
//
// WHY THE STAGED PLAN IS IN MEMORY AND NOT A TABLE
//
//	A review lasts minutes. Persisting it would be a new table, new migrations
//	and a new lifecycle to get wrong, to buy a restart-mid-review case whose
//	honest remedy - upload the file again - costs the user seconds. If reviews
//	ever become long-running, that is the moment to persist, not before.

// ImportSession is one file staged for review.
type ImportSession struct {
	ID       string     `json:"id"`
	TeamID   string     `json:"team_id"`
	SeatID   string     `json:"seat_id"`
	Plan     ImportPlan `json:"plan"`
	StagedAt time.Time  `json:"staged_at"`
	// raw is the file as it arrived. Kept so a correction re-reads the original
	// rather than patching a plan: there is no half-corrected state, and what
	// the user sees is always one coherent reading.
	raw      []byte
	fileName string
}

// ImportDecision is one row a person has to settle, with the line it came from
// so they can look at their own file while deciding.
type ImportDecision struct {
	Index     int              `json:"index"`
	Row       int              `json:"row"`
	Label     string           `json:"label"`
	Org       string           `json:"org,omitempty"`
	Decision  Decision         `json:"decision"`
	Question  string           `json:"question"`
	Options   []MergeCandidate `json:"options,omitempty"`
	Conflicts []FieldConflict  `json:"conflicts,omitempty"`
}

// ImportSummary is the plan in the shape somebody can be told about it.
type ImportSummary struct {
	ID string `json:"id"`
	// Blocked names why this file cannot be imported as it stands, so the
	// answer is "I cannot tell which column is the name" rather than a refusal
	// the user has to guess at.
	Blocked   string            `json:"blocked,omitempty"`
	Header    []string          `json:"header,omitempty"`
	Samples   [][]string        `json:"samples,omitempty"`
	File      string            `json:"file"`
	Rows      int               `json:"rows"`
	Encoding  string            `json:"encoding"`
	Delimiter string            `json:"delimiter"`
	Mapping   map[string]string `json:"mapping"`
	// Unmapped columns are here because a column of phone numbers that silently
	// vanished is the difference between an import and a partial one nobody was
	// told about.
	Unmapped  []string         `json:"unmapped"`
	Counts    map[string]int   `json:"counts"`
	Decisions []ImportDecision `json:"decisions"`
	// SkippedRows groups line numbers by reason, so the answer is "第 7、19、23
	// 行没有姓名" rather than a count the user cannot act on.
	SkippedRows map[string][]int `json:"skipped_rows"`
	// RowsWithoutStrength is why path_find will still be empty afterwards.
	RowsWithoutStrength int `json:"rows_without_strength"`
}

// StageImport reads a file and holds the plan for review. Writes nothing to the
// graph.
func (s *Store) StageImport(v View, fileName string, raw []byte) (ImportSession, error) {
	plan, err := s.PlanImport(v, fileName, raw, ImportOverrides{})
	if err != nil {
		return ImportSession{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := ImportSession{
		ID: s.nextID("im"), TeamID: v.TeamID, SeatID: v.SeatID,
		Plan: plan, StagedAt: s.now(), raw: append([]byte{}, raw...), fileName: fileName,
	}
	s.imports[sess.ID] = &sess
	return sess, nil
}

// RestageImport re-reads a staged file with the user's corrections applied.
//
// The whole file is read again from the original bytes, so the plan that comes
// back is one coherent reading rather than a base plus amendments - which is
// what makes it safe to correct something three times.
func (s *Store) RestageImport(v View, id string, ov ImportOverrides) (ImportSession, error) {
	sess, ok := s.stagedFor(v, id)
	if !ok {
		return ImportSession{}, ErrNotFound
	}
	plan, err := s.PlanImport(v, sess.fileName, sess.raw, ov)
	if err != nil {
		return ImportSession{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.imports[sess.ID]
	held.Plan = plan
	return *held, nil
}

// stagedFor returns a session, or the seat's most recent one when id is empty -
// "what did that file do" almost always means the one just uploaded.
func (s *Store) stagedFor(v View, id string) (*ImportSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id != "" {
		sess, ok := s.imports[id]
		if !ok || sess.TeamID != v.TeamID || sess.SeatID != v.SeatID {
			return nil, false
		}
		return sess, true
	}
	var newest *ImportSession
	for _, sess := range s.imports {
		if sess.TeamID != v.TeamID || sess.SeatID != v.SeatID {
			continue
		}
		if newest == nil || sess.StagedAt.After(newest.StagedAt) ||
			(sess.StagedAt.Equal(newest.StagedAt) && sess.ID > newest.ID) {
			newest = sess
		}
	}
	return newest, newest != nil
}

// ImportSummary describes a staged plan.
func (s *Store) ImportSummary(v View, id string) (ImportSummary, bool) {
	sess, ok := s.stagedFor(v, id)
	if !ok {
		return ImportSummary{}, false
	}
	p := sess.Plan
	sum := ImportSummary{
		ID: sess.ID, Blocked: p.Blocked, Header: p.Header, Samples: p.Samples,
		File: p.File, Rows: p.Rows, Encoding: p.Encoding, Delimiter: p.Delimiter,
		Mapping: p.Mapping, Unmapped: p.Unmapped, Counts: p.Counts(),
		Decisions: []ImportDecision{}, SkippedRows: map[string][]int{},
	}
	for i, prop := range p.Proposals {
		if prop.Decision == DecideCreate || prop.Decision == DecideUpdate {
			if _, rated := p.Strengths[i]; !rated {
				sum.RowsWithoutStrength++
			}
			continue
		}
		sum.Decisions = append(sum.Decisions, ImportDecision{
			Index: i, Row: p.RowOf[i], Label: prop.Candidate.Label, Org: prop.Candidate.Org,
			Decision: prop.Decision, Question: prop.Question,
			Options: prop.Merges, Conflicts: prop.Conflicts,
		})
		if _, rated := p.Strengths[i]; !rated {
			sum.RowsWithoutStrength++
		}
	}
	for _, sk := range p.Skipped {
		sum.SkippedRows[sk.Reason] = append(sum.SkippedRows[sk.Reason], sk.Row)
	}
	for _, rows := range sum.SkippedRows {
		sort.Ints(rows)
	}
	return sum, true
}

// CommitImport applies a staged plan and drops the session.
func (s *Store) CommitImport(v View, id string, res map[int]Resolution, at time.Time) (ImportResult, error) {
	sess, ok := s.stagedFor(v, id)
	if !ok {
		return ImportResult{}, ErrNotFound
	}
	if sess.Plan.Blocked != "" {
		return ImportResult{}, fmt.Errorf("%w: this file cannot be read as it stands (%s); correct the reading first",
			ErrBadArguments, sess.Plan.Blocked)
	}
	out, err := s.ApplyImport(v, sess.Plan, res, at)
	if err != nil {
		return out, err
	}
	s.mu.Lock()
	delete(s.imports, sess.ID)
	s.mu.Unlock()
	return out, nil
}

// DiscardImport throws a staged plan away. A separate call from committing,
// for the same reason declining to decide is separate from deciding.
func (s *Store) DiscardImport(v View, id string) bool {
	sess, ok := s.stagedFor(v, id)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.imports, sess.ID)
	return true
}

// ---- the follow-up that makes an import worth anything ----

// RatingGap is what stands between an import and a working path_find.
//
// After importing two hundred people with no strengths recorded, the graph is
// full and PathsTo still returns NoSeeds. Nothing else in this package will
// ever ask, so this is the thing that has to.
type RatingGap struct {
	Unrated int `json:"unrated"`
	Rated   int `json:"rated"`
	// Ask is a slice of the unrated people, IN NAME ORDER and capped.
	//
	// Deliberately not ordered by anything else. The obvious "helpful" ordering
	// - who sits on the most edges, who would unlock the most routes - is a
	// ranking of people wearing a different hat, and the user knows who they
	// are close to far better than a graph does.
	Ask []ChartPerson `json:"ask"`
}

// RatingAskCap is how many to put in front of somebody at once. Ten is a list a
// person answers; two hundred is a list they close.
const RatingAskCap = 10

// RatingGap reports how much of the graph this seat has said anything about.
func (s *Store) RatingGap(v View, limit int) RatingGap {
	if limit <= 0 {
		limit = RatingAskCap
	}
	rated := map[string]bool{}
	for _, n := range s.Seeds(v) {
		rated[n.ID] = true
	}
	out := RatingGap{Rated: len(rated), Ask: []ChartPerson{}}
	for _, n := range s.Nodes(v, NodeFilter{Kind: KindPerson}) {
		if rated[n.ID] {
			continue
		}
		out.Unrated++
		if len(out.Ask) < limit {
			out.Ask = append(out.Ask, ChartPerson{
				NodeID: n.ID, Label: n.Label, RoleTitle: n.RoleTitle,
				Duty: n.Duty, Corroboration: n.Corroboration(), Unconfirmed: n.Unconfirmed,
			})
		}
	}
	sortPeople(out.Ask)
	return out
}

// applyColumnOverrides lets the user say which column is which when the alias
// table did not know their word for it. A name that matches no header is
// ignored rather than silently mapping to column zero.
func applyColumnOverrides(cols map[Column]int, header []string, ov map[Column]string) {
	for field, want := range ov {
		want = strings.TrimSpace(want)
		if want == "" {
			delete(cols, field)
			continue
		}
		if strings.HasPrefix(want, "#") {
			if n, err := strconv.Atoi(want[1:]); err == nil && n >= 1 && n <= len(header) {
				cols[field] = n - 1
			}
			continue
		}
		for i, h := range header {
			if normHeader(h) == normHeader(want) {
				cols[field] = i
				break
			}
		}
	}
}

// unusedHeaders recomputes what nothing reads, after the overrides.
func unusedHeaders(header []string, cols map[Column]int) []string {
	used := map[int]bool{}
	for _, i := range cols {
		used[i] = true
	}
	var out []string
	for i, h := range header {
		if !used[i] && strings.TrimSpace(h) != "" {
			out = append(out, h)
		}
	}
	return out
}

// mapValue translates what a column contains into what this package expects,
// using the user's own vocabulary. 很熟 is not a defect; it is how they wrote it.
func mapValue(m map[string]string, raw string) string {
	raw = strings.TrimSpace(raw)
	if m == nil || raw == "" {
		return raw
	}
	if v, ok := m[raw]; ok {
		return strings.TrimSpace(v)
	}
	for k, v := range m {
		if normHeader(k) == normHeader(raw) {
			return strings.TrimSpace(v)
		}
	}
	return raw
}
