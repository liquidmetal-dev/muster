package fakegithub_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

const (
	org         = "liquidmetal-dev"
	orgRepo     = "liquidmetal-dev/muster"
	privateRepo = "someone/private"
)

func seed(e env) {
	e.fake.AddOrg(org)
	e.fake.AddRepo(orgRepo)
	e.fake.AddRepo(privateRepo)
}

func jitReq(name string, labels ...string) github.CreateJITConfigRequest {
	return github.CreateJITConfigRequest{Name: name, RunnerGroupID: 1, WorkFolder: new("_work"), Labels: labels}
}

func orgJIT(t *testing.T, e env, name string, labels ...string) *github.JITRunnerConfig {
	t.Helper()
	cfg, _, err := e.client.Actions.CreateOrgJITConfig(ctx(), org, jitReq(name, labels...))
	if err != nil {
		t.Fatalf("CreateOrgJITConfig(%s): %v", name, err)
	}
	return cfg
}

func TestRunnerGroupsDefaultAndAdded(t *testing.T) {
	e := start(t)
	seed(e)
	id, err := e.fake.AddRunnerGroup(org, "gpu")
	if err != nil || id != 2 {
		t.Fatalf("AddRunnerGroup = %d, %v", id, err)
	}
	groups, _, err := e.client.Actions.ListOrganizationRunnerGroups(ctx(), org, nil)
	if err != nil {
		t.Fatalf("ListOrganizationRunnerGroups: %v", err)
	}
	if groups.TotalCount != 2 || len(groups.RunnerGroups) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	g0, g1 := groups.RunnerGroups[0], groups.RunnerGroups[1]
	if g0.GetID() != 1 || g0.GetName() != "Default" || !g0.GetDefault() {
		t.Fatalf("group 0 = %+v", g0)
	}
	if g1.GetID() != 2 || g1.GetName() != "gpu" || g1.GetDefault() {
		t.Fatalf("group 1 = %+v", g1)
	}
}

func TestRunnerGroupsUnknownOrg(t *testing.T) {
	e := start(t)
	_, _, err := e.client.Actions.ListOrganizationRunnerGroups(ctx(), "nope", nil)
	wantStatus(t, err, http.StatusNotFound)
}

func TestAddRunnerGroupErrors(t *testing.T) {
	e := start(t)
	seed(e)
	if _, err := e.fake.AddRunnerGroup("nope", "gpu"); err == nil {
		t.Fatal("unknown org accepted")
	}
	if _, err := e.fake.AddRunnerGroup(org, "Default"); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestOrgJITConfigCreatesOfflineRunner(t *testing.T) {
	e := start(t)
	seed(e)
	cfg := orgJIT(t, e, "muster-a", "self-hosted", "linux")
	r := cfg.GetRunner()
	if r.GetStatus() != "offline" || !r.GetEphemeral() || r.GetID() != 1 || r.GetName() != "muster-a" {
		t.Fatalf("runner %+v", r)
	}
	blob, err := fakegithub.DecodeJITConfig(cfg.GetEncodedJITConfig())
	if err != nil || blob.RunnerName != "muster-a" || blob.RunnerID != 1 {
		t.Fatalf("blob %+v err %v", blob, err)
	}
	got := e.fake.Runners()
	if len(got) != 1 || got[0].Scope != (fakegithub.Scope{Org: org}) || got[0].Status != "offline" {
		t.Fatalf("Runners() = %+v", got)
	}
}

func TestRepoJITConfigCreatesRunner(t *testing.T) {
	e := start(t)
	seed(e)
	cfg, resp, err := e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", jitReq("muster-r", "x"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("CreateRepoJITConfig: %v", err)
	}
	if cfg.GetRunner().GetName() != "muster-r" {
		t.Fatalf("runner %+v", cfg.GetRunner())
	}
	got := e.fake.Runners()
	if len(got) != 1 || got[0].Scope != (fakegithub.Scope{Repo: privateRepo}) {
		t.Fatalf("Runners() = %+v", got)
	}
}

func TestJITConfigUnknownScope(t *testing.T) {
	e := start(t)
	seed(e)
	_, _, err := e.client.Actions.CreateOrgJITConfig(ctx(), "nope", jitReq("a", "x"))
	wantStatus(t, err, http.StatusNotFound)
	_, _, err = e.client.Actions.CreateRepoJITConfig(ctx(), "nope", "nope", jitReq("a", "x"))
	wantStatus(t, err, http.StatusNotFound)
}

func TestJITConfigValidation(t *testing.T) {
	e := start(t)
	seed(e)
	_, _, err := e.client.Actions.CreateOrgJITConfig(ctx(), org, jitReq("", "x"))
	wantStatus(t, err, http.StatusUnprocessableEntity)
	_, _, err = e.client.Actions.CreateOrgJITConfig(ctx(), org, jitReq("a"))
	wantStatus(t, err, http.StatusUnprocessableEntity)
	req := jitReq("a", "x")
	req.RunnerGroupID = 9
	_, _, err = e.client.Actions.CreateOrgJITConfig(ctx(), org, req)
	wantStatus(t, err, http.StatusNotFound)
	req.RunnerGroupID = 2
	_, _, err = e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", req)
	wantStatus(t, err, http.StatusNotFound)
}

func TestJITConfigDuplicateName(t *testing.T) {
	e := start(t)
	seed(e)
	orgJIT(t, e, "dup", "x")
	_, _, err := e.client.Actions.CreateOrgJITConfig(ctx(), org, jitReq("dup", "x"))
	wantStatus(t, err, http.StatusConflict)
	_, resp, err := e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", jitReq("dup", "x"))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("same name in another scope: %v", err)
	}
}

func TestListRunnersScoped(t *testing.T) {
	e := start(t)
	seed(e)
	orgJIT(t, e, "muster-org", "x")
	if _, _, err := e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", jitReq("muster-repo", "x")); err != nil {
		t.Fatal(err)
	}
	orgList, _, err := e.client.Actions.ListOrganizationRunners(ctx(), org, nil)
	if err != nil || orgList.TotalCount != 1 || orgList.Runners[0].GetName() != "muster-org" {
		t.Fatalf("org runners %+v err %v", orgList, err)
	}
	repoList, _, err := e.client.Actions.ListRunners(ctx(), "someone", "private", nil)
	if err != nil || repoList.TotalCount != 1 || repoList.Runners[0].GetName() != "muster-repo" {
		t.Fatalf("repo runners %+v err %v", repoList, err)
	}
	if orgList.Runners[0].GetID() == repoList.Runners[0].GetID() {
		t.Fatal("runner ids collide across scopes")
	}
}

func TestListRunnersNameFilter(t *testing.T) {
	e := start(t)
	seed(e)
	orgJIT(t, e, "muster-a", "x")
	orgJIT(t, e, "muster-b", "x")
	list, _, err := e.client.Actions.ListOrganizationRunners(ctx(), org, &github.ListRunnersOptions{Name: new("muster-b")})
	if err != nil || list.TotalCount != 1 || list.Runners[0].GetName() != "muster-b" {
		t.Fatalf("filtered %+v err %v", list, err)
	}
	list, _, err = e.client.Actions.ListOrganizationRunners(ctx(), org, &github.ListRunnersOptions{Name: new("nope")})
	if err != nil || list.TotalCount != 0 || len(list.Runners) != 0 {
		t.Fatalf("unknown name %+v err %v", list, err)
	}
}

func TestListRunnersPagination(t *testing.T) {
	e := start(t)
	seed(e)
	for i := range 7 {
		orgJIT(t, e, fmt.Sprintf("muster-%d", i), "x")
	}
	page := func(n, per int) (*github.Runners, *github.Response) {
		list, resp, err := e.client.Actions.ListOrganizationRunners(ctx(), org,
			&github.ListRunnersOptions{ListOptions: github.ListOptions{Page: n, PerPage: per}})
		if err != nil {
			t.Fatalf("page %d: %v", n, err)
		}
		return list, resp
	}
	l1, r1 := page(1, 3)
	if len(l1.Runners) != 3 || l1.TotalCount != 7 || r1.NextPage != 2 || r1.LastPage != 3 {
		t.Fatalf("page1: %d runners, total %d, next %d, last %d", len(l1.Runners), l1.TotalCount, r1.NextPage, r1.LastPage)
	}
	l2, r2 := page(2, 3)
	if len(l2.Runners) != 3 || r2.NextPage != 3 {
		t.Fatalf("page2: %d runners, next %d", len(l2.Runners), r2.NextPage)
	}
	l3, r3 := page(3, 3)
	if len(l3.Runners) != 1 || r3.NextPage != 0 {
		t.Fatalf("page3: %d runners, next %d", len(l3.Runners), r3.NextPage)
	}
	past, rp := page(9, 3)
	if len(past.Runners) != 0 || rp.NextPage != 0 {
		t.Fatalf("page 9: %d runners, next %d", len(past.Runners), rp.NextPage)
	}
}

func TestListRunnersPerPageClampedTo100(t *testing.T) {
	e := start(t)
	seed(e)
	for i := range 101 {
		orgJIT(t, e, fmt.Sprintf("muster-%03d", i), "x")
	}
	list, resp, err := e.client.Actions.ListOrganizationRunners(ctx(), org,
		&github.ListRunnersOptions{ListOptions: github.ListOptions{Page: 1, PerPage: 500}})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Runners) != 100 || list.TotalCount != 101 || resp.NextPage != 2 {
		t.Fatalf("per_page 500: %d runners, total %d, next %d", len(list.Runners), list.TotalCount, resp.NextPage)
	}
}

func TestDeleteRunner(t *testing.T) {
	e := start(t)
	seed(e)
	id := orgJIT(t, e, "muster-a", "x").GetRunner().GetID()
	resp, err := e.client.Actions.RemoveOrganizationRunner(ctx(), org, id)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %v", err)
	}
	if len(e.fake.Runners()) != 0 {
		t.Fatalf("runner still listed: %+v", e.fake.Runners())
	}
	_, err = e.client.Actions.RemoveOrganizationRunner(ctx(), org, id)
	wantStatus(t, err, http.StatusNotFound)

	repoCfg, _, err := e.client.Actions.CreateRepoJITConfig(ctx(), "someone", "private", jitReq("muster-r", "x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.client.Actions.RemoveOrganizationRunner(ctx(), org, repoCfg.GetRunner().GetID())
	wantStatus(t, err, http.StatusNotFound)
}

func TestDeleteRunnerUnknownScope(t *testing.T) {
	e := start(t)
	_, err := e.client.Actions.RemoveOrganizationRunner(ctx(), "nope", 1)
	wantStatus(t, err, http.StatusNotFound)
	_, err = e.client.Actions.RemoveRunner(ctx(), "nope", "nope", 1)
	wantStatus(t, err, http.StatusNotFound)
}
