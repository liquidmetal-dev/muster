package fakegithub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Route patterns. They are the keys for FailNext, Stall and RequestCount.
const (
	PatternAccessToken   = "POST /app/installations/{id}/access_tokens" //nolint:gosec // a route pattern, not a credential
	PatternInstallation  = "GET /app/installations/{id}"
	PatternUser          = "GET /user"
	PatternOrgJITConfig  = "POST /orgs/{org}/actions/runners/generate-jitconfig"
	PatternRepoJITConfig = "POST /repos/{owner}/{repo}/actions/runners/generate-jitconfig"
	PatternOrgRunners    = "GET /orgs/{org}/actions/runners"
	PatternRepoRunners   = "GET /repos/{owner}/{repo}/actions/runners"
	PatternOrgRunnerDel  = "DELETE /orgs/{org}/actions/runners/{id}"
	PatternRepoRunnerDel = "DELETE /repos/{owner}/{repo}/actions/runners/{id}"
	PatternRunnerGroups  = "GET /orgs/{org}/actions/runner-groups"
	PatternRuns          = "GET /repos/{owner}/{repo}/actions/runs"
	PatternRunJobs       = "GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs"
)

const (
	apiV3Prefix      = "/api/v3"
	documentationURL = "https://docs.github.com/rest"
	rateLimit        = 5000
	rateRemaining    = 4999
)

// authReq is what a route requires from the caller.
type authReq int

const (
	authAPI  authReq = iota // installation token or PAT
	authJWT                 // App JWT
	authUser                // PAT; an installation token is 403
)

type handlerFunc func(w http.ResponseWriter, r *http.Request, a authInfo)

type ctxKey struct{}

func authFrom(ctx context.Context) authInfo {
	a, _ := ctx.Value(ctxKey{}).(authInfo)
	return a
}

// route registers fn under pattern with its authentication requirement.
func (s *Server) route(pattern string, req authReq, fn handlerFunc) {
	s.auth[pattern] = req
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		fn(w, r, authFrom(r.Context()))
	})
}

func (s *Server) registerRoutes() {
	s.route(PatternAccessToken, authJWT, s.handleAccessToken)
	s.route(PatternInstallation, authJWT, s.handleInstallation)
	s.route(PatternUser, authUser, s.handleUser)
	s.registerRunnerRoutes()
}

// statusWriter records the status code written by a handler.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// ServeHTTP runs the pipeline: strip /api/v3, stall, fault, rate-limit
// headers, authenticate, dispatch, log.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logged := Request{Method: r.Method, Path: r.URL.Path}
	r = stripPrefix(r)
	_, pattern := s.mux.Handler(r)
	if _, known := s.auth[pattern]; !known {
		pattern = ""
	}
	logged.Pattern = pattern

	if d, ok := s.popStall(pattern); ok {
		time.Sleep(d)
	}
	sw := &statusWriter{ResponseWriter: w}
	s.writeRateHeaders(sw)

	if f, ok := s.popFault(pattern); ok {
		for k, v := range f.Header {
			sw.Header()[k] = v
		}
		writeError(sw, f.Status, f.Message)
		logged.Status = sw.status
		logged.Auth = "none"
		s.logRequest(logged)
		return
	}

	a := s.authenticate(r)
	logged.Auth = a.kind
	switch {
	case pattern == "":
		writeError(sw, http.StatusNotFound, "Not Found")
	case !s.authorized(pattern, a, sw):
		// authorized wrote the error
	default:
		s.mux.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
	}
	logged.Status = sw.status
	s.logRequest(logged)
}

// authorized checks a against the route's requirement and writes the error
// response when it fails.
func (s *Server) authorized(pattern string, a authInfo, w http.ResponseWriter) bool {
	switch s.auth[pattern] {
	case authJWT:
		if a.kind != "jwt" {
			writeError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
			return false
		}
	case authUser:
		switch a.kind {
		case "pat":
		case "installation":
			writeError(w, http.StatusForbidden, "Resource not accessible by integration")
			return false
		default:
			writeError(w, http.StatusUnauthorized, "Bad credentials")
			return false
		}
	default:
		if a.kind != "installation" && a.kind != "pat" {
			writeError(w, http.StatusUnauthorized, "Bad credentials")
			return false
		}
	}
	return true
}

func stripPrefix(r *http.Request) *http.Request {
	if r.URL.Path != apiV3Prefix && !strings.HasPrefix(r.URL.Path, apiV3Prefix+"/") {
		return r
	}
	r2 := new(http.Request)
	*r2 = *r
	u := *r.URL
	u.Path = strings.TrimPrefix(u.Path, apiV3Prefix)
	if u.RawPath != "" {
		u.RawPath = strings.TrimPrefix(u.RawPath, apiV3Prefix)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	r2.URL = &u
	return r2
}

func (s *Server) writeRateHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(rateLimit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(rateRemaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(s.clock.Now().Add(time.Hour).Unix(), 10))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message, "documentation_url": documentationURL})
}

const (
	defaultPerPage = 30
	maxPerPage     = 100
)

// paginate reads page and per_page from r and returns the half-open index
// range [lo, hi) of a list of total items plus the Link header value, which
// is empty when everything fits on one page.
func paginate(r *http.Request, total int) (lo, hi int, link string) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 {
		per = defaultPerPage
	}
	if per > maxPerPage {
		per = maxPerPage
	}
	lo = min((page-1)*per, total)
	hi = min(lo+per, total)
	last := max((total+per-1)/per, 1)
	if last <= 1 {
		return lo, hi, ""
	}
	pageURL := func(n int) string {
		u := *r.URL
		u.Scheme = "http"
		if r.TLS != nil {
			u.Scheme = "https"
		}
		u.Host = r.Host
		v := url.Values{}
		for k, vals := range q {
			v[k] = vals
		}
		v.Set("page", strconv.Itoa(n))
		v.Set("per_page", strconv.Itoa(per))
		u.RawQuery = v.Encode()
		return fmt.Sprintf("<%s>; rel=%q", u.String(), "")
	}
	rel := func(n int, name string) string { return strings.Replace(pageURL(n), `rel=""`, `rel="`+name+`"`, 1) }
	var links []string
	if page > 1 {
		links = append(links, rel(min(page-1, last), "prev"), rel(1, "first"))
	}
	if page < last {
		links = append(links, rel(page+1, "next"), rel(last, "last"))
	}
	return lo, hi, strings.Join(links, ", ")
}

func setLink(w http.ResponseWriter, link string) {
	if link != "" {
		w.Header().Set("Link", link)
	}
}
