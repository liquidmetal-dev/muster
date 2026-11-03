package github

import (
	"context"

	gogithub "github.com/google/go-github/v93/github"
)

const (
	endpointRuns    = "GET /repos/{owner}/{repo}/actions/runs"
	endpointRunJobs = "GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs"

	statusQueued = "queued"
)

// QueuedJob is a workflow job that waits for a runner.
type QueuedJob struct {
	JobID, RunID int64
	Labels       []string
	// Repository is "owner/name".
	Repository string
}

// ListQueuedJobs returns the queued jobs of the queued workflow runs of an
// owner/name repository. Label matching belongs to the caller.
func (c *Client) ListQueuedJobs(ctx context.Context, repo string) ([]QueuedJob, error) {
	s, err := RepoScope(repo)
	if err != nil {
		return nil, err
	}
	runOpts := &gogithub.ListWorkflowRunsOptions{Status: statusQueued, ListOptions: gogithub.ListOptions{PerPage: perPage}}
	var out []QueuedJob
	for {
		runs, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(withEndpoint(ctx, endpointRuns), s.Owner, s.Repo, runOpts)
		if err != nil {
			return nil, mapError(err)
		}
		for _, run := range runs.WorkflowRuns {
			jobs, err := c.runJobs(ctx, s, run.GetID())
			if err != nil {
				return nil, err
			}
			out = append(out, queuedOnly(repo, run.GetID(), jobs)...)
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		runOpts.Page = resp.NextPage
	}
}

func (c *Client) runJobs(ctx context.Context, s Scope, runID int64) ([]*gogithub.WorkflowJob, error) {
	opts := &gogithub.ListWorkflowJobsOptions{Filter: "latest", ListOptions: gogithub.ListOptions{PerPage: perPage}}
	var out []*gogithub.WorkflowJob
	for {
		page, resp, err := c.gh.Actions.ListWorkflowJobs(withEndpoint(ctx, endpointRunJobs), s.Owner, s.Repo, runID, opts)
		if err != nil {
			return nil, mapError(err)
		}
		out = append(out, page.Jobs...)
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// queuedOnly drops jobs that already started: a queued run can hold jobs in
// any state.
func queuedOnly(repo string, runID int64, jobs []*gogithub.WorkflowJob) []QueuedJob {
	var out []QueuedJob
	for _, j := range jobs {
		if j.GetStatus() != statusQueued {
			continue
		}
		out = append(out, QueuedJob{JobID: j.GetID(), RunID: runID, Labels: j.Labels, Repository: repo})
	}
	return out
}
