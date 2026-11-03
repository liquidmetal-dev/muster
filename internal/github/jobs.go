package github

import (
	"context"
	"time"

	gogithub "github.com/google/go-github/v93/github"
)

const (
	endpointRuns    = "GET /repos/{owner}/{repo}/actions/runs"
	endpointRunJobs = "GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs"

	statusQueued     = "queued"
	statusInProgress = "in_progress"
)

// QueuedJob is a workflow job that waits for a runner.
type QueuedJob struct {
	JobID, RunID int64
	Labels       []string
	// Repository is "owner/name".
	Repository string
	CreatedAt  time.Time
}

// ListQueuedJobs returns the queued jobs of the queued and in-progress
// workflow runs of an owner/name repository, each job once. A run is in
// progress once its first job starts, and it can still hold queued jobs.
// Queued runs are listed first: runs only move forward, so a run that starts
// between the two lists is in one of them. Label matching belongs to the
// caller.
func (c *Client) ListQueuedJobs(ctx context.Context, repo string) ([]QueuedJob, error) {
	s, err := RepoScope(repo)
	if err != nil {
		return nil, err
	}
	var out []QueuedJob
	seen := map[int64]bool{}
	for _, status := range []string{statusQueued, statusInProgress} {
		err := c.eachRun(ctx, s, status, func(runID int64) error {
			jobs, err := c.runJobs(ctx, s, runID)
			if err != nil {
				return err
			}
			for _, j := range queuedOnly(repo, runID, jobs) {
				if !seen[j.JobID] {
					seen[j.JobID] = true
					out = append(out, j)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) eachRun(ctx context.Context, s Scope, status string, fn func(runID int64) error) error {
	opts := &gogithub.ListWorkflowRunsOptions{Status: status, ListOptions: gogithub.ListOptions{PerPage: perPage}}
	for {
		runs, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(withEndpoint(ctx, endpointRuns), s.Owner, s.Repo, opts)
		if err != nil {
			return mapError(err)
		}
		for _, run := range runs.WorkflowRuns {
			if err := fn(run.GetID()); err != nil {
				return err
			}
		}
		if resp.NextPage == 0 {
			return nil
		}
		opts.Page = resp.NextPage
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
		out = append(out, QueuedJob{JobID: j.GetID(), RunID: runID, Labels: j.Labels, Repository: repo, CreatedAt: j.GetCreatedAt().Time})
	}
	return out
}
