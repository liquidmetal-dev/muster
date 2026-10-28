package fakegithub_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func TestUnknownRouteIs404JSON(t *testing.T) {
	e := start(t)
	resp := rawGet(t, e.url+"/nope", "Bearer "+e.token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body struct{ Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Message != "Not Found" {
		t.Fatalf("body %+v err %v", body, err)
	}
	reqs := e.fake.Requests()
	last := reqs[len(reqs)-1]
	if last.Path != "/nope" || last.Pattern != "" || last.Status != 404 {
		t.Fatalf("logged %+v", last)
	}
}

func TestAPIv3PrefixStripped(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(9*time.Minute))
	c, err := github.NewClient(github.WithEnterpriseURLs(e.url, e.url), github.WithAuthToken(jwt))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	inst, _, err := c.Apps.GetInstallation(ctx(), installationID)
	if err != nil {
		t.Fatalf("GetInstallation via /api/v3: %v", err)
	}
	if inst.GetID() != installationID {
		t.Fatalf("installation %+v", inst)
	}
}

func TestRateLimitHeadersOnEveryResponse(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(9*time.Minute))
	_, resp, err := jwtClient(t, e.url, jwt).Apps.GetInstallation(ctx(), installationID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if resp.Rate.Limit != 5000 || resp.Rate.Remaining != 4999 {
		t.Fatalf("rate %+v", resp.Rate)
	}
	if !resp.Rate.Reset.Time.Equal(now.Add(time.Hour)) {
		t.Fatalf("reset %v, want %v", resp.Rate.Reset.Time, now.Add(time.Hour))
	}
}

func TestRequestLogRecordsPatternAndAuth(t *testing.T) {
	e := start(t) // start mints one token: exactly one request so far
	reqs := e.fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests logged, want 1", len(reqs))
	}
	want := fakegithub.Request{Method: "POST", Path: "/app/installations/456/access_tokens",
		Pattern: fakegithub.PatternAccessToken, Status: 201, Auth: "jwt"}
	if reqs[0] != want {
		t.Fatalf("got %+v, want %+v", reqs[0], want)
	}
	if e.fake.RequestCount(fakegithub.PatternAccessToken) != 1 {
		t.Fatalf("RequestCount = %d", e.fake.RequestCount(fakegithub.PatternAccessToken))
	}
}

func TestFailNextReturnsFaultOnce(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	c := jwtClient(t, e.url, signJWT(t, e.key, "123", now, now.Add(9*time.Minute)))
	e.fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{
		Status: 429, Header: http.Header{"Retry-After": []string{"1"}}, Message: "rate limited"})
	_, resp, err := c.Apps.GetInstallation(ctx(), installationID)
	wantStatus(t, err, 429)
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After %q", resp.Header.Get("Retry-After"))
	}
	if _, resp, err = c.Apps.GetInstallation(ctx(), installationID); err != nil || resp.StatusCode != 200 {
		t.Fatalf("second call: %v %v", err, resp)
	}
}

func TestFailNextStacks(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	c := jwtClient(t, e.url, signJWT(t, e.key, "123", now, now.Add(9*time.Minute)))
	e.fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 500, Message: "one"})
	e.fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{Status: 502, Message: "two"})
	_, _, err := c.Apps.GetInstallation(ctx(), installationID)
	wantStatus(t, err, 500)
	_, _, err = c.Apps.GetInstallation(ctx(), installationID)
	wantStatus(t, err, 502)
	if _, _, err = c.Apps.GetInstallation(ctx(), installationID); err != nil {
		t.Fatalf("third call: %v", err)
	}
}

func TestFailNextPrimaryRateLimit(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	c := jwtClient(t, e.url, signJWT(t, e.key, "123", now, now.Add(9*time.Minute)))
	e.fake.FailNext(fakegithub.PatternInstallation, fakegithub.Fault{
		Status: 403, Header: http.Header{"X-RateLimit-Remaining": []string{"0"}}, Message: "API rate limit exceeded"})
	_, _, err := c.Apps.GetInstallation(ctx(), installationID)
	var rl *github.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("got %T: %v, want *github.RateLimitError", err, err)
	}
}

func TestStallDelaysResponse(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	c := jwtClient(t, e.url, signJWT(t, e.key, "123", now, now.Add(9*time.Minute)))
	e.fake.Stall(fakegithub.PatternInstallation, 200*time.Millisecond)
	began := time.Now()
	if _, _, err := c.Apps.GetInstallation(ctx(), installationID); err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if el := time.Since(began); el < 200*time.Millisecond {
		t.Fatalf("elapsed %v, want >= 200ms", el)
	}
}

func TestAPIv3PaginationLinksKeepPrefix(t *testing.T) {
	e := start(t)
	seed(e)
	for i := range 4 {
		orgJIT(t, e, fmt.Sprintf("muster-%d", i), "x")
	}
	c, err := github.NewClient(github.WithEnterpriseURLs(e.url, e.url), github.WithAuthToken(e.token))
	if err != nil {
		t.Fatal(err)
	}
	_, resp, err := c.Actions.ListOrganizationRunners(ctx(), org,
		&github.ListRunnersOptions{ListOptions: github.ListOptions{Page: 1, PerPage: 3}})
	if err != nil {
		t.Fatal(err)
	}
	link := resp.Header.Get("Link")
	if resp.NextPage != 2 || !strings.Contains(link, e.url+"/api/v3/orgs/liquidmetal-dev/actions/runners?") {
		t.Fatalf("next %d, link %q", resp.NextPage, link)
	}
}
