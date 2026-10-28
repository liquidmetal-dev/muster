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
