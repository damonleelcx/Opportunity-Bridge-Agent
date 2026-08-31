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

const DefaultApolloEndpoint = "https://api.apollo.io/api/v1/mixed_people/api_search"

// Apollo is Apollo.io's People Search.
//
// ‼️ It is NOT free, and the widely repeated claim that it is comes from
// conflating two things. The endpoint costs no CREDITS, which is true. Access to
// it is gated by PLAN, which is the part that matters: a free-plan key gets
// 403 "The api/v1/mixed_people/api_search API is not included in your Free plan".
// Measured against a real free-tier key on 2026-08-31. Any paid plan includes it.
//
// What still makes it worth having, on a paid plan:
//
//   - Its search response is already de-identified BY THE VENDOR: it returns
//     last_name_obfuscated, and has_email / has_city / has_direct_phone as
//     BOOLEANS rather than values. Contact details require the paid enrichment
//     endpoints, which this adapter does not call and must not.
//
// What it is not: a source of blue-collar workers. Apollo is a B2B sales
// database of people with work email addresses at companies, strongest in the
// US. 数控操作工 and 养老护理员 do not have work email addresses, and the caveat
// says so rather than letting an empty result read as an absence of workers.
type Apollo struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
}

func NewApollo(endpoint, key string) *Apollo {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultApolloEndpoint
	}
	return &Apollo{
		Endpoint: endpoint, APIKey: key,
		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (a *Apollo) Name() string { return "Apollo.io" }

type apolloRequest struct {
	PersonTitles    []string `json:"person_titles,omitempty"`
	PersonLocations []string `json:"person_locations,omitempty"`
	Keywords        string   `json:"q_keywords,omitempty"`
	Page            int      `json:"page"`
	PerPage         int      `json:"per_page"`
}

type apolloResponse struct {
	// Apollo puts the reason for a refusal in one of these. Both are read so the
	// operator is told WHY - a bare "403" sends somebody to check a key that is
	// fine, when the real answer is usually that their plan has no API access.
	Error        string         `json:"error"`
	ErrorMessage string         `json:"error_message"`
	TotalEntries int            `json:"total_entries"`
	People       []apolloPerson `json:"people"`
	Pagination   *struct {
		TotalEntries int `json:"total_entries"`
	} `json:"pagination"`
}

// apolloPerson lists ONLY what this adapter reads.
//
// first_name is decodable from the wire and is deliberately NOT declared here:
// Apollo obfuscates the surname but hands over the given name, and a given name
// is still a person's name. Nothing in a Lead has anywhere to put it.
type apolloPerson struct {
	Title    string `json:"title"`
	City     string `json:"city"`
	State    string `json:"state"`
	Country  string `json:"country"`
	HasEmail bool   `json:"has_email"`
}

func (a *Apollo) Find(ctx context.Context, q Query) (Found, error) {
	if strings.TrimSpace(a.APIKey) == "" {
		return Found{}, fmt.Errorf("APOLLO_NOT_CONFIGURED: no API key; set OBA_APOLLO_API_KEY or leave this provider out")
	}
	payload := apolloRequest{Page: 1, PerPage: q.limit()}
	if t := strings.TrimSpace(q.Title); t != "" {
		payload.PersonTitles = []string{t}
	}
	if loc := strings.TrimSpace(firstNonEmpty(q.Region, q.Country)); loc != "" {
		payload.PersonLocations = []string{loc}
	}
	payload.Keywords = strings.Join(dedupeSkills(q.Skills, 6), " ")
	if payload.Keywords == "" && len(payload.PersonTitles) == 0 && len(payload.PersonLocations) == 0 {
		return Found{}, fmt.Errorf("APOLLO_QUERY_EMPTY: a search needs at least one of skills, title or region")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Found{}, fmt.Errorf("APOLLO_REQUEST_ENCODE: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Found{}, fmt.Errorf("APOLLO_REQUEST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("x-api-key", a.APIKey)

	res, err := a.client().Do(req)
	if err != nil {
		return Found{}, fmt.Errorf("APOLLO_UNREACHABLE: %w", err)
	}
	defer res.Body.Close()

	var decoded apolloResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return Found{}, fmt.Errorf("APOLLO_DECODE: HTTP %d and the body did not parse: %w", res.StatusCode, err)
	}
	if res.StatusCode != http.StatusOK {
		reason := strings.TrimSpace(firstNonEmpty(decoded.ErrorMessage, decoded.Error))
		if reason == "" {
			reason = "no reason given"
		}
		hint := ""
		if res.StatusCode == http.StatusForbidden {
			hint = " — a 403 here is usually the PLAN, not the key: Apollo gates API access by tier, " +
				"and People Search is unavailable on plans without it. Check the key's plan at " +
				"https://app.apollo.io/#/settings/integrations/api before regenerating anything"
		}
		return Found{}, fmt.Errorf("APOLLO_HTTP_%d: %s%s", res.StatusCode, reason, hint)
	}

	total := decoded.TotalEntries
	if total == 0 && decoded.Pagination != nil {
		total = decoded.Pagination.TotalEntries
	}
	out := Found{
		Total:   total,
		AtLeast: total,
		// Apollo will not display beyond 50,000 records, so a total at or above
		// the cap is a floor rather than a count, and an answer must say "at
		// least". Reporting it flat would overstate the precision of a number
		// that the vendor itself refuses to page through.
		Truncated: total >= apolloDisplayCap,
	}
	for _, person := range decoded.People {
		out.Leads = append(out.Leads, Lead{
			Source:       a.Name(),
			Title:        strings.TrimSpace(person.Title),
			Region:       apolloRegion(person),
			Reachable:    person.HasEmail,
			ConsentBasis: apolloConsentBasis,
			Caveat:       apolloCaveat,
		})
	}
	return out, nil
}

const apolloDisplayCap = 50000

const apolloConsentBasis = "None on file. Apollo compiles this for B2B sales and marketing; " +
	"it is not consent given by this person to be approached about a job, and it is not a PIPL basis for contacting them."

const apolloCaveat = "Apollo indexes people with work email addresses at companies, strongest in the US. " +
	"It carries no skills field, so matching here is on job title only. A near-zero count for manual, care or " +
	"service trades reflects what Apollo indexes, NOT how many such workers exist."

func apolloRegion(p apolloPerson) string {
	parts := make([]string, 0, 3)
	for _, s := range []string{p.City, p.State, p.Country} {
		if t := strings.TrimSpace(s); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (a *Apollo) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}
