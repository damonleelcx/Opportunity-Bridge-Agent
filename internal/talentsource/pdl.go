package talentsource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultPDLEndpoint is the production Person Search API.
//
// PDL also runs a sandbox at https://sandbox.api.peopledatalabs.com/v5/person/search
// which answers with SYNTHETIC records at zero credit cost. Point OBA_PDL_API_URL
// at it to exercise this adapter without touching a real person's data - which is
// the right way to develop against it, and the only way to run it in CI.
const DefaultPDLEndpoint = "https://api.peopledatalabs.com/v5/person/search"

// PDL is People Data Labs' Person Search API.
//
// Why it is here at all, given its coverage of this product's population is
// thin: it is the only one of the evaluated vendors with a `skills` field, and
// skills is what candidate_search matches on. That makes it the only one whose
// answer to "how many people with these skills exist" is about skills rather
// than about job titles.
//
// The number to keep in mind when reading anything it returns: `skills` is
// populated for about 8.6% of its 2.47B records. A zero from PDL means "not in
// the 8.6%" at least as often as it means "nobody like that exists", and the
// caveat below says so rather than letting a silence read as a finding.
type PDL struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

func NewPDL(endpoint, key string) *PDL {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultPDLEndpoint
	}
	return &PDL{
		Endpoint: endpoint, APIKey: key,
		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (p *PDL) Name() string { return "People Data Labs" }

type pdlRequest struct {
	SQL  string `json:"sql"`
	Size int    `json:"size"`
}

type pdlResponse struct {
	Status int         `json:"status"`
	Total  int         `json:"total"`
	Data   []pdlPerson `json:"data"`
	Error  *struct {
		Message any `json:"message"`
	} `json:"error"`
}

// pdlPerson lists ONLY the fields this adapter reads.
//
// work_email and mobile_phone are deliberately absent. PDL returns them and this
// struct refuses to decode them, so they cannot reach a Lead even by accident -
// the same reason candidateCard is a projection rather than a domain.Profile.
// linkedin_url is absent for the same reason: it is an identity, not a job fact.
type pdlPerson struct {
	JobTitle        masked     `json:"job_title"`
	LocationName    masked     `json:"location_name"`
	LocationCountry masked     `json:"location_country"`
	Skills          maskedList `json:"skills"`
	YearsExperience float64    `json:"inferred_years_experience"`
	// WorkEmail is read for its PRESENCE only. Nothing here copies .Value out of
	// it, and the Lead this builds has no field to put an address in.
	WorkEmail masked `json:"work_email"`
}

// masked decodes a field that PDL returns in TWO different shapes.
//
// Production answers with the value: "job_title": "cnc machine operator".
// The SANDBOX answers with a presence flag: "job_title": true — it reports
// whether a field is populated without inventing synthetic personal data for it.
//
// A plain string field therefore fails to decode the ENTIRE response against the
// sandbox, which is the endpoint PDL tells you to develop against and the one
// .env.example here recommends. Found by `make talent-smoke`; no fixture written
// from the production shape could have caught it, which is the argument for
// having a live smoke at all.
//
// Present is carried separately from Value because for a contact field the
// presence is the whole answer and the value must never travel.
type masked struct {
	Value   string
	Present bool
}

func (m *masked) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	switch {
	case trimmed == "" || trimmed == "null" || trimmed == "false":
		*m = masked{}
	case trimmed == "true":
		*m = masked{Present: true}
	case trimmed[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		v = strings.TrimSpace(v)
		*m = masked{Value: v, Present: v != ""}
	default:
		// A number or an object. Presence is all this package can honestly say.
		*m = masked{Present: true}
	}
	return nil
}

// maskedList is the array equivalent: production returns ["cnc","welding"], the
// sandbox returns true.
type maskedList struct {
	Values  []string
	Present bool
}

func (m *maskedList) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	switch {
	case trimmed == "" || trimmed == "null" || trimmed == "false":
		*m = maskedList{}
	case trimmed == "true":
		*m = maskedList{Present: true}
	case trimmed[0] == '[':
		var v []string
		if err := json.Unmarshal(b, &v); err != nil {
			// A heterogeneous array is not something this package will guess at.
			*m = maskedList{Present: true}
			return nil
		}
		*m = maskedList{Values: v, Present: len(v) > 0}
	default:
		*m = maskedList{Present: true}
	}
	return nil
}

func (p *PDL) Find(ctx context.Context, q Query) (Found, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return Found{}, fmt.Errorf("PDL_NOT_CONFIGURED: no API key; set OBA_PDL_API_KEY or leave this provider out")
	}
	sql, err := pdlSQL(q)
	if err != nil {
		return Found{}, err
	}
	body, err := json.Marshal(pdlRequest{SQL: sql, Size: q.limit()})
	if err != nil {
		return Found{}, fmt.Errorf("PDL_REQUEST_ENCODE: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Found{}, fmt.Errorf("PDL_REQUEST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", p.APIKey)

	res, err := p.client().Do(req)
	if err != nil {
		return Found{}, fmt.Errorf("PDL_UNREACHABLE: %w", err)
	}
	defer res.Body.Close()

	var decoded pdlResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return Found{}, fmt.Errorf("PDL_DECODE: HTTP %d and the body did not parse: %w", res.StatusCode, err)
	}
	// 404 is PDL's "no records matched", which is an answer and not a failure.
	if res.StatusCode == http.StatusNotFound {
		return Found{}, nil
	}
	if res.StatusCode != http.StatusOK {
		msg := ""
		if decoded.Error != nil {
			msg = fmt.Sprint(decoded.Error.Message)
		}
		return Found{}, fmt.Errorf("PDL_HTTP_%d: %s", res.StatusCode, msg)
	}

	out := Found{Total: decoded.Total, AtLeast: decoded.Total}
	for _, person := range decoded.Data {
		region := person.LocationName.Value
		if region == "" {
			region = person.LocationCountry.Value
		}
		out.Leads = append(out.Leads, Lead{
			Source: p.Name(),
			Title:  strings.TrimSpace(person.JobTitle.Value),
			Region: strings.TrimSpace(region),
			Skills: dedupeSkills(person.Skills.Values, 8),
			Years:  person.YearsExperience,
			// Presence only. The address is never read out of masked.Value.
			Reachable:    person.WorkEmail.Present,
			ConsentBasis: pdlConsentBasis,
			Caveat:       pdlCaveat,
		})
	}
	return out, nil
}

const pdlConsentBasis = "None on file. People Data Labs compiles this from public and licensed sources; " +
	"it is not consent given by this person to be approached, and it is not a PIPL basis for contacting them."

// The second sentence is not a hedge, it is an observation. Sampling
// skills=cnc + country=china on 2026-08-31 returned 4,261 records whose titles
// were "associate", "chief operating officer", "co-president" and "manager" -
// engineers and managers who list CNC beside AutoCAD, ANSYS and FEA. Not one was
// a 数控操作工. Reporting that count to an employer as "CNC operators" would be
// false, so the caveat says what the records actually are.
const pdlCaveat = "PDL's skills field is populated for roughly 8.6% of its records, and its index is " +
	"LinkedIn-weighted, which covers mainland China thinly since LinkedIn's local service closed in 2023. " +
	"In practice a China skills match returns ENGINEERS AND MANAGERS who list the skill alongside CAD and " +
	"simulation tools, not people who do the job on a shop floor — describe them that way, never as operators. " +
	"A low count here means 'few records of this shape in this index' — it does NOT mean few such workers exist."

// pdlSQL builds the WHERE clause.
//
// It is built from a fixed set of columns rather than passed through, because a
// caller-composed clause would be an injection surface into somebody else's
// database, and because the columns this product may filter on are a policy
// decision - the same five as candidate_search, and nothing about a person's
// age, household registration, gender or situation.
func pdlSQL(q Query) (string, error) {
	var where []string
	for _, s := range dedupeSkills(q.Skills, 6) {
		where = append(where, fmt.Sprintf("skills LIKE %s", pdlQuote("%"+s+"%")))
	}
	if t := strings.TrimSpace(q.Title); t != "" {
		where = append(where, fmt.Sprintf("job_title LIKE %s", pdlQuote("%"+t+"%")))
	}
	if c := strings.TrimSpace(q.Country); c != "" {
		where = append(where, fmt.Sprintf("location_country = %s", pdlQuote(strings.ToLower(c))))
	}
	if r := strings.TrimSpace(q.Region); r != "" {
		where = append(where, fmt.Sprintf("location_name LIKE %s", pdlQuote("%"+strings.ToLower(r)+"%")))
	}
	if len(where) == 0 {
		return "", fmt.Errorf("PDL_QUERY_EMPTY: a search needs at least one of skills, title, country or region; " +
			"an unfiltered query would return an arbitrary slice of two billion people")
	}
	return "SELECT * FROM person WHERE " + strings.Join(where, " AND "), nil
}

// pdlQuote single-quotes a literal and doubles any quote inside it.
func pdlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (p *PDL) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}
