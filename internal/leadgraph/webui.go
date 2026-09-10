package leadgraph

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"time"
)

// The one screen, and the seam where authentication is somebody else's job.
//
// WHY THE TEXT LIST IS SERVER-RENDERED AND THE GRAPH IS NOT
//
//	The graph is an enhancement. The page is the list. A reader on a slow link,
//	with script blocked, printing the page, or hitting a rendering bug still
//	gets every person, every group, and every 待确认 badge - because the server
//	already put them in the markup.
//
//	This is the concrete form of the PRD's "图谱必须能降级". A fallback that has
//	to be fetched and rendered is not a fallback; it fails in exactly the
//	conditions it exists for.
//
// WHY Handler TAKES A resolve FUNCTION INSTEAD OF DOING AUTH
//
//	This package has no idea who the caller is, and inventing a scheme here -
//	a header, a query parameter, a cookie - would be a security decision made
//	in the wrong place and then depended on. The host supplies the seam; a
//	request it cannot resolve gets nothing.

//go:embed web/*
var webFiles embed.FS

var graphTemplate = template.Must(template.ParseFS(webFiles, "web/graph.html.tmpl"))

// pageData is what the template sees. JSON is the same snapshot the script
// reads, so the picture and the list cannot come from different reads.
type pageData struct {
	Snapshot
	JSON template.JS
	// Leads are rendered into the HTML and are deliberately NOT in JSON: the
	// board belongs on the product's own screen, and nowhere a machine can take
	// a copy of it. See lead.go, constraint 3.
	Leads []Lead
}

// WindowDays is the stated convention printed next to every window, so the
// number on screen carries its own definition rather than being a figure the
// reader has to trust.
func (pageData) WindowDays() int { return int(LeadWindow.Hours() / 24) }

// Handler serves the graph screen. resolve turns a request into a View; when it
// cannot, the request is refused rather than served a default.
func Handler(s *Store, resolve func(*http.Request) (View, bool)) http.Handler {
	mux := http.NewServeMux()

	// Health is readable from outside and needs no credential: a liveness probe
	// cannot hold one, and a health signal only in the logs is a health signal
	// nobody reads. It says whether writes are still being KEPT - a process that
	// answers requests while silently failing to persist is the failure worth
	// catching, and it looks perfectly healthy from a port check.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		status, code := "ok", http.StatusOK
		if s.Degraded() {
			status, code = "degraded", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status, "writes_persisted": !s.Degraded(),
		})
	})

	mux.HandleFunc("/graph.css", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "web/graph.css", "text/css; charset=utf-8")
	})
	mux.HandleFunc("/graph.js", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "web/graph.js", "text/javascript; charset=utf-8")
	})

	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		v, ok := resolve(r)
		if !ok {
			http.Error(w, "UNRESOLVED_VIEW: this request does not identify a team and a seat", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(s.Snapshot(v, time.Now().UTC()))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		v, ok := resolve(r)
		if !ok {
			http.Error(w, "UNRESOLVED_VIEW: this request does not identify a team and a seat", http.StatusUnauthorized)
			return
		}
		snap := s.Snapshot(v, time.Now().UTC())
		raw, err := json.Marshal(snap)
		if err != nil {
			http.Error(w, "SNAPSHOT_ENCODE_FAILED: the page cannot be rendered from this data", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The script tag holds JSON, not JavaScript: it is read with
		// JSON.parse rather than executed, so a label containing a quote is a
		// label, not a way into the page.
		page := pageData{Snapshot: snap, JSON: template.JS(raw), Leads: s.LeadBoard(v, snap.At)}
		if err := graphTemplate.Execute(w, page); err != nil {
			// Headers are already out; the reader gets a truncated page rather
			// than a wrong one. Nothing to recover, and nothing to hide.
			return
		}
	})
	return mux
}

func serveAsset(w http.ResponseWriter, name, ctype string) {
	b, err := webFiles.ReadFile(name)
	if err != nil {
		http.Error(w, "ASSET_MISSING: "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(b)
}

// WebAsset exposes a shipped asset so the fences can read the file that
// actually ships rather than a copy written for the test.
func WebAsset(name string) (string, error) {
	b, err := webFiles.ReadFile("web/" + name)
	return string(b), err
}
