package fakegithub_test

import (
	"net/http"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func queue(t *testing.T, e env, repo string, labels ...string) fakegithub.Job {
	t.Helper()
	job, err := e.fake.QueueJob(repo, labels)
	if err != nil {
		t.Fatalf("QueueJob(%s): %v", repo, err)
	}
	return job
}

func TestQueueJobStoresQueued(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "self-hosted", "linux")
	jobs := e.fake.Jobs()
	if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Status != "queued" || jobs[0].RunID == 0 {
		t.Fatalf("Jobs() = %+v", jobs)
	}
	if len(jobs[0].Labels) != 2 || jobs[0].Labels[0] != "self-hosted" || jobs[0].Repo != orgRepo {
		t.Fatalf("job %+v", jobs[0])
	}
}

func TestQueueJobUnknownRepo(t *testing.T) {
	e := start(t)
	if _, err := e.fake.QueueJob("nope/nope", []string{"x"}); err == nil {
		t.Fatal("unknown repo accepted")
	}
	if len(e.fake.Jobs()) != 0 || len(e.fake.Deliveries()) != 0 {
		t.Fatal("state changed for an unknown repo")
	}
}

func TestQueueJobEmptyLabels(t *testing.T) {
	e := start(t)
	seed(e)
	if _, err := e.fake.QueueJob(orgRepo, nil); err == nil {
		t.Fatal("empty labels accepted")
	}
}

func TestListQueuedRuns(t *testing.T) {
	e := start(t)
	seed(e)
	queue(t, e, orgRepo, "x")
	queue(t, e, orgRepo, "y")
	runs, _, err := e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "liquidmetal-dev", "muster",
		&github.ListWorkflowRunsOptions{Status: "queued"})
	if err != nil || runs.GetTotalCount() != 2 || len(runs.WorkflowRuns) != 2 {
		t.Fatalf("runs %+v err %v", runs, err)
	}
	for _, r := range runs.WorkflowRuns {
		if r.GetStatus() != "queued" {
			t.Fatalf("run %+v", r)
		}
	}
	other, _, err := e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "someone", "private",
		&github.ListWorkflowRunsOptions{Status: "queued"})
	if err != nil || other.GetTotalCount() != 0 {
		t.Fatalf("other repo runs %+v err %v", other, err)
	}
	_, _, err = e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "nope", "nope", nil)
	wantStatus(t, err, http.StatusNotFound)
}

func TestListRunJobsReturnsLabels(t *testing.T) {
	e := start(t)
	seed(e)
	job := queue(t, e, orgRepo, "self-hosted", "muster")
	jobs, _, err := e.client.Actions.ListWorkflowJobs(ctx(), "liquidmetal-dev", "muster", job.RunID, nil)
	if err != nil || jobs.GetTotalCount() != 1 {
		t.Fatalf("jobs %+v err %v", jobs, err)
	}
	j := jobs.Jobs[0]
	if j.GetID() != job.ID || j.GetStatus() != "queued" || len(j.Labels) != 2 || j.Labels[1] != "muster" {
		t.Fatalf("job %+v", j)
	}
	_, _, err = e.client.Actions.ListWorkflowJobs(ctx(), "someone", "private", job.RunID, nil)
	wantStatus(t, err, http.StatusNotFound)
	_, _, err = e.client.Actions.ListWorkflowJobs(ctx(), "liquidmetal-dev", "muster", 999999, nil)
	wantStatus(t, err, http.StatusNotFound)
}

func TestRunsPagination(t *testing.T) {
	e := start(t)
	seed(e)
	for range 4 {
		queue(t, e, orgRepo, "x")
	}
	p1, r1, err := e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "liquidmetal-dev", "muster",
		&github.ListWorkflowRunsOptions{ListOptions: github.ListOptions{Page: 1, PerPage: 3}})
	if err != nil || len(p1.WorkflowRuns) != 3 || r1.NextPage != 2 {
		t.Fatalf("page 1: %d runs next %d err %v", len(p1.WorkflowRuns), r1.NextPage, err)
	}
	p2, r2, err := e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "liquidmetal-dev", "muster",
		&github.ListWorkflowRunsOptions{ListOptions: github.ListOptions{Page: 2, PerPage: 3}})
	if err != nil || len(p2.WorkflowRuns) != 1 || r2.NextPage != 0 {
		t.Fatalf("page 2: %d runs next %d err %v", len(p2.WorkflowRuns), r2.NextPage, err)
	}
}

func runsWithStatus(t *testing.T, e env, status string) []*github.WorkflowRun {
	t.Helper()
	runs, _, err := e.client.Actions.ListRepositoryWorkflowRuns(ctx(), "liquidmetal-dev", "muster",
		&github.ListWorkflowRunsOptions{Status: status})
	if err != nil {
		t.Fatalf("list %s runs: %v", status, err)
	}
	return runs.WorkflowRuns
}

func TestRunStatusFollowsItsJobs(t *testing.T) {
	e := start(t)
	seed(e)
	a := queue(t, e, orgRepo, "x")
	b, err := e.fake.QueueJobInRun(a.RunID, []string{"y"})
	if err != nil {
		t.Fatalf("QueueJobInRun: %v", err)
	}
	if b.RunID != a.RunID || b.Repo != orgRepo || b.Status != "queued" {
		t.Fatalf("job b %+v", b)
	}
	if runs := runsWithStatus(t, e, "queued"); len(runs) != 1 || runs[0].GetID() != a.RunID {
		t.Fatalf("queued runs %+v", runs)
	}
	jobs, _, err := e.client.Actions.ListWorkflowJobs(ctx(), "liquidmetal-dev", "muster", a.RunID, nil)
	if err != nil || jobs.GetTotalCount() != 2 || len(jobs.Jobs) != 2 || jobs.Jobs[0].GetID() != a.ID || jobs.Jobs[1].GetID() != b.ID {
		t.Fatalf("run jobs %+v err %v", jobs, err)
	}

	if err := e.fake.StartJob(a.ID); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if runs := runsWithStatus(t, e, "in_progress"); len(runs) != 1 || runs[0].GetID() != a.RunID {
		t.Fatalf("in_progress runs %+v", runs)
	}
	if runs := runsWithStatus(t, e, "queued"); len(runs) != 0 {
		t.Fatalf("queued runs after start %+v", runs)
	}

	c, err := e.fake.QueueJobInRun(a.RunID, []string{"z"})
	if err != nil {
		t.Fatalf("QueueJobInRun: %v", err)
	}
	if err := e.fake.CancelJob(c.ID); err != nil {
		t.Fatalf("CancelJob(c): %v", err)
	}
	if err := e.fake.CancelJob(b.ID); err != nil {
		t.Fatalf("CancelJob(b): %v", err)
	}
	if runs := runsWithStatus(t, e, "in_progress"); len(runs) != 1 {
		t.Fatalf("in_progress runs after cancels %+v", runs)
	}
	if runs := runsWithStatus(t, e, "completed"); len(runs) != 0 {
		t.Fatalf("completed runs %+v", runs)
	}
}

func TestQueueJobInRunUnknownRun(t *testing.T) {
	e := start(t)
	seed(e)
	if _, err := e.fake.QueueJobInRun(999999, []string{"x"}); err == nil {
		t.Fatal("unknown run accepted")
	}
	if len(e.fake.Jobs()) != 0 || len(e.fake.Deliveries()) != 0 {
		t.Fatal("state changed for an unknown run")
	}
}

func TestQueueJobInRunEmptyLabels(t *testing.T) {
	e := start(t)
	seed(e)
	a := queue(t, e, orgRepo, "x")
	if _, err := e.fake.QueueJobInRun(a.RunID, nil); err == nil {
		t.Fatal("empty labels accepted")
	}
}

func TestStartJobRejectsNonQueued(t *testing.T) {
	e := start(t)
	seed(e)
	a := queue(t, e, orgRepo, "x")
	if err := e.fake.StartJob(a.ID); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if err := e.fake.StartJob(a.ID); err == nil {
		t.Fatal("second StartJob accepted")
	}
	if err := e.fake.StartJob(999999); err == nil {
		t.Fatal("StartJob of an unknown job accepted")
	}
}

func TestCancelJobRejectsNonQueued(t *testing.T) {
	e := start(t)
	seed(e)
	a := queue(t, e, orgRepo, "x")
	if err := e.fake.StartJob(a.ID); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if err := e.fake.CancelJob(a.ID); err == nil {
		t.Fatal("CancelJob of a started job accepted")
	}
}

func TestCancelJobCompletesRun(t *testing.T) {
	e := start(t)
	seed(e)
	a := queue(t, e, orgRepo, "x")
	if err := e.fake.CancelJob(a.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	runs := runsWithStatus(t, e, "completed")
	if len(runs) != 1 || runs[0].GetConclusion() != "cancelled" {
		t.Fatalf("completed runs %+v", runs)
	}
	if j := e.fake.Jobs()[0]; j.Status != "completed" || j.Conclusion != "cancelled" {
		t.Fatalf("job %+v", j)
	}
}
