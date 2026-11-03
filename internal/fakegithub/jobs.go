package fakegithub

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/go-github/v93/github"
)

// Job is a snapshot of one workflow job.
type Job struct {
	ID, RunID int64
	// Repo is "owner/name".
	Repo   string
	Labels []string
	// Status is "queued", "in_progress" or "completed".
	Status string
	// Conclusion is "", "success", "failure" or "cancelled".
	Conclusion string
	RunnerID   int64
	RunnerName string
}

type jobState struct {
	Job
	// announced is set once the queued delivery returned. Pairing ignores a
	// job that is not announced yet, so in_progress never overtakes queued.
	announced     bool
	runnerGroupID int64
	createdAt     time.Time
	startedAt     time.Time
	completedAt   time.Time
}

const (
	jobQueued     = "queued"
	jobInProgress = "in_progress"
	jobCompleted  = "completed"

	conclusionCancelled = "cancelled"
)

// QueueJob stores a queued job for repo with labels as its own run, delivers
// the signed workflow_job.queued webhook, and returns the job. It fails when
// repo is not seeded or labels is empty.
func (s *Server) QueueJob(repo string, labels []string) (Job, error) {
	if len(labels) == 0 {
		return Job{}, fmt.Errorf("fakegithub: QueueJob(%q): labels must not be empty", repo)
	}
	s.mu.Lock()
	if _, ok := s.repos[repo]; !ok {
		s.mu.Unlock()
		return Job{}, fmt.Errorf("fakegithub: QueueJob: unknown repo %q", repo)
	}
	js := s.addJobLocked(s.nextIDLocked(), repo, labels)
	return s.announce(js), nil
}

// QueueJobInRun adds a queued job with labels to the existing run runID, in
// that run's repository, delivers the signed workflow_job.queued webhook,
// and returns the job. It fails when the run is unknown or labels is empty.
func (s *Server) QueueJobInRun(runID int64, labels []string) (Job, error) {
	if len(labels) == 0 {
		return Job{}, fmt.Errorf("fakegithub: QueueJobInRun(%d): labels must not be empty", runID)
	}
	s.mu.Lock()
	ids, ok := s.runs[runID]
	if !ok {
		s.mu.Unlock()
		return Job{}, fmt.Errorf("fakegithub: QueueJobInRun: unknown run %d", runID)
	}
	js := s.addJobLocked(runID, s.jobs[ids[0]].Repo, labels)
	return s.announce(js), nil
}

func (s *Server) addJobLocked(runID int64, repo string, labels []string) *jobState {
	js := &jobState{Job: Job{
		RunID:  runID,
		Repo:   repo,
		Labels: append([]string(nil), labels...),
		Status: jobQueued,
	}, createdAt: s.clock.Now()}
	js.ID = s.nextIDLocked()
	s.jobs[js.ID] = js
	s.runs[runID] = append(s.runs[runID], js.ID)
	return js
}

// announce is called with s.mu held and releases it. It delivers the
// queued webhook and only then lets runners pair with the job.
func (s *Server) announce(js *jobState) Job {
	snap := js.snapshot()
	copyJS := *js
	s.mu.Unlock()

	s.deliver(jobQueued, copyJS)

	s.mu.Lock()
	js.announced = true
	s.mu.Unlock()
	s.wake()
	return snap
}

// StartJob moves the queued job id to in_progress without a runner and
// delivers the signed workflow_job.in_progress webhook.
func (s *Server) StartJob(id int64) error {
	return s.moveQueued("StartJob", id, func(js *jobState) {
		js.Status = jobInProgress
		js.startedAt = s.clock.Now()
	})
}

// CancelJob moves the queued job id to completed with conclusion cancelled
// and delivers the signed workflow_job.completed webhook.
func (s *Server) CancelJob(id int64) error {
	return s.moveQueued("CancelJob", id, func(js *jobState) {
		js.Status = jobCompleted
		js.Conclusion = conclusionCancelled
		js.completedAt = s.clock.Now()
	})
}

// moveQueued accepts only a queued job: a started job can hold a runner
// whose lifecycle would complete it again.
func (s *Server) moveQueued(op string, id int64, move func(*jobState)) error {
	s.mu.Lock()
	js, ok := s.jobs[id]
	if !ok || js.Status != jobQueued {
		s.mu.Unlock()
		return fmt.Errorf("fakegithub: %s: job %d is not queued", op, id)
	}
	move(js)
	copyJS := *js
	s.mu.Unlock()

	s.deliver(copyJS.Status, copyJS)
	s.wake()
	return nil
}

// Jobs returns a snapshot of every job, ordered by id.
func (s *Server) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

func (j *jobState) snapshot() Job {
	snap := j.Job
	snap.Labels = append([]string(nil), j.Labels...)
	return snap
}

func (s *Server) registerJobRoutes() {
	s.route(PatternRuns, authAPI, s.handleRuns)
	s.route(PatternRunJobs, authAPI, s.handleRunJobs)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	status := r.URL.Query().Get("status")
	s.mu.Lock()
	var runs []*github.WorkflowRun
	for runID, ids := range s.runs {
		first := s.jobs[ids[0]]
		runStatus, conclusion := s.runStateLocked(ids)
		if first.Repo != scope.Repo || (status != "" && runStatus != status) {
			continue
		}
		runs = append(runs, &github.WorkflowRun{
			ID:         new(runID),
			Name:       new("run"),
			Status:     new(runStatus),
			Conclusion: optString(conclusion),
			Repository: &github.Repository{FullName: new(first.Repo)},
			CreatedAt:  &github.Timestamp{Time: first.createdAt},
		})
	}
	s.mu.Unlock()
	sort.Slice(runs, func(i, k int) bool { return runs[i].GetID() < runs[k].GetID() })
	lo, hi, link := paginate(r, len(runs))
	setLink(w, link)
	writeJSON(w, http.StatusOK, &github.WorkflowRuns{TotalCount: new(len(runs)), WorkflowRuns: runs[lo:hi]})
}

// runStateLocked derives a run's status from its jobs as GitHub does: a run
// is in progress once any job has started and until every job completes.
// A completed run takes the conclusion of its last completed job.
func (s *Server) runStateLocked(ids []int64) (status, conclusion string) {
	var queued, running, completed int
	var last *jobState
	for _, id := range ids {
		j := s.jobs[id]
		switch j.Status {
		case jobQueued:
			queued++
		case jobInProgress:
			running++
		default:
			completed++
			if last == nil || !j.completedAt.Before(last.completedAt) {
				last = j
			}
		}
	}
	switch {
	case completed == len(ids):
		return jobCompleted, last.Conclusion
	case running > 0 || completed > 0:
		return jobInProgress, ""
	default:
		return jobQueued, ""
	}
}

func (s *Server) handleRunJobs(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	runID, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	s.mu.Lock()
	var out []*github.WorkflowJob
	if ids := s.runs[runID]; len(ids) > 0 && s.jobs[ids[0]].Repo == scope.Repo {
		for _, id := range ids {
			out = append(out, toGitHubJob(s.jobs[id]))
		}
	}
	s.mu.Unlock()
	if err != nil || out == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, &github.Jobs{TotalCount: new(len(out)), Jobs: out})
}

func toGitHubJob(j *jobState) *github.WorkflowJob {
	wj := &github.WorkflowJob{
		ID:         new(j.ID),
		RunID:      new(j.RunID),
		Name:       new("job"),
		Status:     new(j.Status),
		Conclusion: optString(j.Conclusion),
		Labels:     append([]string(nil), j.Labels...),
		CreatedAt:  &github.Timestamp{Time: j.createdAt},
	}
	if j.RunnerName != "" {
		wj.RunnerID = new(j.RunnerID)
		wj.RunnerName = new(j.RunnerName)
		wj.RunnerGroupID = new(j.runnerGroupID)
	}
	if !j.startedAt.IsZero() {
		wj.StartedAt = &github.Timestamp{Time: j.startedAt}
	}
	if !j.completedAt.IsZero() {
		wj.CompletedAt = &github.Timestamp{Time: j.completedAt}
	}
	return wj
}

func optString(v string) *string {
	if v == "" {
		return nil
	}
	return new(v)
}
