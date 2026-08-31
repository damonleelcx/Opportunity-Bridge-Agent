package talentsource

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The fixtures below are the vendors' real wire shapes, INCLUDING the fields
// this package must not pass on. That is the point of them: a fixture that omits
// work_email cannot prove work_email is dropped.

const pdlBody = `{
  "status": 200,
  "total": 1240,
  "data": [
    {
      "full_name": "wang jianguo",
      "job_title": "cnc machine operator",
      "location_name": "chengdu, sichuan, china",
      "location_country": "china",
      "skills": ["cnc", "welding", "cnc", "  ", "milling"],
      "inferred_years_experience": 6,
      "work_email": "wang.jianguo@example.com",
      "mobile_phone": "+8613800000000",
      "linkedin_url": "linkedin.com/in/wangjianguo",
      "job_company_name": "Example Manufacturing Co"
    }
  ]
}`

const apolloBody = `{
  "total_entries": 240,
  "people": [
    {
      "id": "abc123",
      "first_name": "Jianguo",
      "last_name_obfuscated": "W███",
      "title": "Production Supervisor",
      "city": "Chengdu",
      "state": "Sichuan",
      "country": "China",
      "has_email": true,
      "has_direct_phone": "true",
      "organization": {"name": "Example Manufacturing Co"}
    }
  ]
}`

// PDL's SANDBOX answers with presence flags instead of values, for every
// PII-bearing field. It is a second wire format, and the repo rule is that each
// one gets its own fixture - this one exists because a production-shaped fixture
// let a bug ship that made the entire sandbox response fail to decode.
const pdlSandboxBody = `{
  "status": 200,
  "total": 214,
  "data": [
    {
      "full_name": true,
      "job_title": "engineer, building services",
      "location_name": true,
      "location_country": "united states",
      "skills": true,
      "inferred_years_experience": null,
      "work_email": true,
      "mobile_phone": false,
      "linkedin_url": true
    }
  ]
}`

func serving(t *testing.T, status int, body string, capture *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			b, _ := io.ReadAll(r.Body)
			*capture = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The single most important test here. A vendor hands over names, emails, phone
// numbers and profile URLs; none of them may survive into a Lead.
//
// It asserts on the SERIALISED lead, not the struct, because that is what
// reaches the model and then the recruiter. A field added to Lead that nobody
// meant to expose fails here.
func TestVendorIdentifiersNeverSurviveIntoALead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		make   func(url string) Provider
		banned map[string]string
	}{
		{
			name: "PDL", body: pdlBody,
			make: func(u string) Provider { return NewPDL(u, "test-key") },
			banned: map[string]string{
				"wang jianguo":                "the person's name",
				"wang.jianguo@example.com":    "a work email — the vendor returns it, we must not",
				"+8613800000000":              "a mobile number",
				"linkedin.com/in/wangjianguo": "a profile URL, which is an identity not a job fact",
				"Example Manufacturing Co":    "the employer, which narrows re-identification",
			},
		},
		{
			name: "Apollo", body: apolloBody,
			make: func(u string) Provider { return NewApollo(u, "test-key") },
			banned: map[string]string{
				"Jianguo":                  "the given name — Apollo obfuscates the surname and hands over this",
				"Example Manufacturing Co": "the employer",
				"abc123":                   "the vendor's person id, a stable handle back to the individual",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serving(t, http.StatusOK, tc.body, nil)
			found, err := tc.make(srv.URL).Find(context.Background(), Query{Skills: []string{"cnc"}, Country: "china"})
			if err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(found.Leads) != 1 {
				t.Fatalf("expected one lead, got %d", len(found.Leads))
			}
			raw, err := json.Marshal(found.Leads[0])
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			body := string(raw)
			for needle, why := range tc.banned {
				if strings.Contains(body, needle) {
					t.Errorf("a recruiter can see %q\n  why that is wrong: %s\n  in: %s", needle, why, body)
				}
			}
			// And the lead must still carry what it is FOR, so this cannot be
			// passed by returning an empty record.
			lead := found.Leads[0]
			if lead.ConsentBasis == "" || lead.Caveat == "" {
				t.Errorf("a lead without a consent basis or caveat cannot be told apart from a scraped one: %+v", lead)
			}
			if lead.Region == "" || lead.Title == "" {
				t.Errorf("the lead carries no job facts, so it answers nothing: %+v", lead)
			}
			if !lead.Reachable {
				t.Errorf("the fixture says the vendor holds a contact route; the boolean should say so: %+v", lead)
			}
		})
	}
}

func TestPDLReadsTheFieldsItClaimsTo(t *testing.T) {
	srv := serving(t, http.StatusOK, pdlBody, nil)
	found, err := NewPDL(srv.URL, "k").Find(context.Background(), Query{Skills: []string{"cnc"}})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.Total != 1240 {
		t.Errorf("total is the answer this package exists for; got %d want 1240", found.Total)
	}
	lead := found.Leads[0]
	if lead.Years != 6 {
		t.Errorf("years: got %v want 6", lead.Years)
	}
	// Duplicates and blanks in the vendor's skill list must not reach the reader.
	want := []string{"cnc", "welding", "milling"}
	if len(lead.Skills) != len(want) {
		t.Fatalf("skills: got %v want %v", lead.Skills, want)
	}
	for i := range want {
		if lead.Skills[i] != want[i] {
			t.Errorf("skills: got %v want %v", lead.Skills, want)
		}
	}
}

// PDL answers 404 when nothing matched. Treating that as an error would turn
// "nobody like that in this index" into "the lookup broke", and the recruiter
// would be told the wrong thing about the market.
func TestPDLNotFoundIsAnAnswerNotAFailure(t *testing.T) {
	srv := serving(t, http.StatusNotFound, `{"status":404,"error":{"message":"No records found"}}`, nil)
	found, err := NewPDL(srv.URL, "k").Find(context.Background(), Query{Skills: []string{"cnc"}})
	if err != nil {
		t.Fatalf("a 404 from PDL must be an empty answer, not an error: %v", err)
	}
	if found.Total != 0 || len(found.Leads) != 0 {
		t.Errorf("expected an empty result, got %+v", found)
	}
}

// The SQL is composed here, so it is this package's job not to let a quote in a
// user-supplied skill change the shape of somebody else's query.
func TestPDLQueryIsBuiltFromAFixedColumnSetAndQuotesLiterals(t *testing.T) {
	sql, err := pdlSQL(Query{Skills: []string{"o'brien welding"}, Country: "China", Region: "Chengdu"})
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	if !strings.Contains(sql, "''") {
		t.Errorf("a quote in a skill was not escaped: %s", sql)
	}
	for _, col := range []string{"skills LIKE", "location_country =", "location_name LIKE"} {
		if !strings.Contains(sql, col) {
			t.Errorf("missing %q in %s", col, sql)
		}
	}
	// An unfiltered search would return an arbitrary slice of two billion people.
	if _, err := pdlSQL(Query{}); err == nil {
		t.Error("an empty query was accepted")
	}
}

func TestApolloSendsTitleAndLocationAndNeverAsksForContacts(t *testing.T) {
	var sent string
	srv := serving(t, http.StatusOK, apolloBody, &sent)
	found, err := NewApollo(srv.URL, "k").Find(context.Background(),
		Query{Title: "production supervisor", Region: "Chengdu", Skills: []string{"cnc"}})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.Total != 240 {
		t.Errorf("total: got %d want 240", found.Total)
	}
	for _, want := range []string{"production supervisor", "Chengdu", "cnc"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the request did not carry %q: %s", want, sent)
		}
	}
	// This adapter must never call the paid enrichment endpoints, and must never
	// ask the search endpoint to reveal contacts.
	for _, forbidden := range []string{"reveal_personal_emails", "reveal_phone_number", "enrich"} {
		if strings.Contains(sent, forbidden) {
			t.Errorf("the request asked for contact details (%q): %s", forbidden, sent)
		}
	}
}

// Apollo will not page beyond 50,000 records, so a total at the cap is a floor.
// Reporting it flat would overstate the precision of a number the vendor itself
// refuses to page through.
func TestApolloSaysWhenTheTotalIsAFloor(t *testing.T) {
	srv := serving(t, http.StatusOK, `{"total_entries":50000,"people":[]}`, nil)
	found, err := NewApollo(srv.URL, "k").Find(context.Background(), Query{Title: "engineer"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !found.Truncated {
		t.Error("a total at the display cap must be reported as 'at least', not as a count")
	}
}

// One vendor being down must not make the market look smaller than it is. The
// other still answers, and the error names who was silent.
func TestChainSurvivesOneProviderFailingAndSaysWhichOne(t *testing.T) {
	ok := serving(t, http.StatusOK, apolloBody, nil)
	broken := serving(t, http.StatusInternalServerError, `{}`, nil)

	chain := Chain{NewApollo(ok.URL, "k"), NewPDL(broken.URL, "k")}
	found, err := chain.Find(context.Background(), Query{Title: "supervisor", Skills: []string{"cnc"}})
	if err == nil || !strings.Contains(err.Error(), "People Data Labs") {
		t.Errorf("a silent vendor must be named, got: %v", err)
	}
	if len(found.Leads) != 1 || found.Total != 240 {
		t.Errorf("the working vendor's answer was lost: %+v", found)
	}
	if found.Leads[0].ID != "lead-001" {
		t.Errorf("leads must be numbered for citation, got %q", found.Leads[0].ID)
	}
}

// A provider with no key must say so rather than returning an empty result that
// reads as "nobody matched".
func TestUnconfiguredProvidersRefuseRatherThanReturnEmpty(t *testing.T) {
	if _, err := NewPDL("", "").Find(context.Background(), Query{Skills: []string{"cnc"}}); err == nil ||
		!strings.Contains(err.Error(), "PDL_NOT_CONFIGURED") {
		t.Errorf("keyless PDL did not refuse: %v", err)
	}
	if _, err := NewApollo("", "").Find(context.Background(), Query{Title: "x"}); err == nil ||
		!strings.Contains(err.Error(), "APOLLO_NOT_CONFIGURED") {
		t.Errorf("keyless Apollo did not refuse: %v", err)
	}
}

// The sandbox format must decode, and must not be mistaken for real values.
//
// Regression fence for the defect `make talent-smoke` found: job_title and
// location_name were plain strings, so `"location_name": true` made the WHOLE
// response fail with a decode error, against the exact endpoint .env.example
// tells people to develop on.
func TestPDLSandboxPresenceFlagsDecodeAndCarryNoValues(t *testing.T) {
	srv := serving(t, http.StatusOK, pdlSandboxBody, nil)
	found, err := NewPDL(srv.URL, "k").Find(context.Background(), Query{Skills: []string{"cnc"}})
	if err != nil {
		t.Fatalf("the sandbox wire format did not decode: %v", err)
	}
	if found.Total != 214 || len(found.Leads) != 1 {
		t.Fatalf("got total=%d leads=%d, want 214/1", found.Total, len(found.Leads))
	}
	lead := found.Leads[0]
	// A field the sandbox masked has no value, and must not become the string
	// "true" - which is what a naive any-to-string coercion would produce.
	if lead.Region == "true" || lead.Region == "false" {
		t.Errorf("a presence flag was rendered as a value: region=%q", lead.Region)
	}
	if len(lead.Skills) != 0 {
		t.Errorf("masked skills produced values from nothing: %v", lead.Skills)
	}
	// The one place a flag IS the answer: the vendor holds a contact route.
	if !lead.Reachable {
		t.Error(`"work_email": true means the vendor holds a route; the boolean should say so`)
	}
	// A field the sandbox DID send as a value still comes through.
	if lead.Title != "engineer, building services" {
		t.Errorf("a real value was lost: title=%q", lead.Title)
	}
	if lead.Region != "united states" {
		t.Errorf("location_country should fill in when location_name is masked: region=%q", lead.Region)
	}
}

// mobile_phone is false in the sandbox fixture and a value in the production
// one. Neither may reach a Lead: the struct does not declare it at all.
func TestPDLNeverDecodesMobilePhoneInEitherFormat(t *testing.T) {
	for name, body := range map[string]string{"production": pdlBody, "sandbox": pdlSandboxBody} {
		srv := serving(t, http.StatusOK, body, nil)
		found, err := NewPDL(srv.URL, "k").Find(context.Background(), Query{Skills: []string{"cnc"}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := json.Marshal(found.Leads)
		if strings.Contains(string(raw), "13800000000") || strings.Contains(string(raw), "8613800000000") {
			t.Errorf("%s: a phone number reached a lead: %s", name, raw)
		}
	}
}

// Adding two vendors' totals double-counts anybody in both indexes. The honest
// combined figure is a range: at least the largest single index, at most the sum.
//
// This was the first version's actual bug — Chain summed and reported one number.
func TestCombinedTotalIsARangeNotASum(t *testing.T) {
	pdl := serving(t, http.StatusOK, `{"status":200,"total":4261,"data":[
	  {"job_title":"cnc machinist","location_name":"chengdu","skills":["cnc"],"work_email":"a@b.com"}]}`, nil)
	apollo := serving(t, http.StatusOK, `{"total_entries":900,"people":[
	  {"title":"Production Supervisor","city":"Chengdu","country":"China","has_email":true}]}`, nil)

	found, err := Chain{NewPDL(pdl.URL, "k"), NewApollo(apollo.URL, "k")}.
		Find(context.Background(), Query{Skills: []string{"cnc"}, Title: "cnc", Country: "china"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.AtLeast != 4261 {
		t.Errorf("lower bound must be the largest single index: got %d want 4261", found.AtLeast)
	}
	if found.Total != 5161 {
		t.Errorf("upper bound must be the sum: got %d want 5161", found.Total)
	}
	if found.AtLeast == found.Total {
		t.Error("with two vendors reporting, the bounds must differ — a single number hides the overlap")
	}
}

// A combined search must say WHICH vendor found what, and must distinguish a
// vendor that found nothing from one that refused. Both contribute zero, and
// only the breakdown tells them apart — this is exactly the live situation:
// PDL answers on its free tier, Apollo 403s on the free plan.
func TestCombinedResultNamesEachVendorIncludingTheRefusedOne(t *testing.T) {
	pdl := serving(t, http.StatusOK, `{"status":200,"total":4261,"data":[
	  {"job_title":"cnc machinist","location_name":"chengdu","skills":["cnc"],"work_email":"a@b.com"}]}`, nil)
	refused := serving(t, http.StatusForbidden,
		`{"error_message":"The api/v1/mixed_people/api_search API is not included in your Free plan"}`, nil)

	found, err := Chain{NewPDL(pdl.URL, "k"), NewApollo(refused.URL, "k")}.
		Find(context.Background(), Query{Skills: []string{"cnc"}, Title: "cnc"})
	if err == nil || !strings.Contains(err.Error(), "Apollo") {
		t.Errorf("the refused vendor must be named in the error: %v", err)
	}
	if len(found.PerVendor) != 2 {
		t.Fatalf("both vendors must appear in the breakdown, got %d: %+v", len(found.PerVendor), found.PerVendor)
	}
	byName := map[string]VendorResult{}
	for _, v := range found.PerVendor {
		byName[v.Name] = v
	}
	if got := byName["People Data Labs"]; got.Total != 4261 || got.Error != "" {
		t.Errorf("the working vendor was misreported: %+v", got)
	}
	a := byName["Apollo.io"]
	if a.Error == "" {
		t.Error("a refused vendor with no reason is indistinguishable from one that found nobody")
	}
	if !strings.Contains(a.Error, "Free plan") {
		t.Errorf("the vendor's own reason must survive to the reader: %q", a.Error)
	}
	if a.Total != 0 {
		t.Errorf("a refused vendor must not contribute a count: %+v", a)
	}
	// And the range must be built from the vendor that actually answered.
	if found.AtLeast != 4261 || found.Total != 4261 {
		t.Errorf("a refusal must not inflate or deflate the range: at_least=%d at_most=%d", found.AtLeast, found.Total)
	}
	// Each vendor's caveat has to travel, or the reader cannot weigh the number.
	if byName["People Data Labs"].Caveat == "" {
		t.Error("a vendor count arrived with no caveat to read it against")
	}
}
