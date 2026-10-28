package fakegithub

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/go-github/v93/github"
)

// Job is a snapshot of one workflow job. Each job is the only job of its run.
type Job struct {
	ID, RunID int64
	// Repo is "owner/name".
	Repo   string
	Labels []string
	// Status is "queued", "in_progress" or "completed".
	Status string
	// Conclusion is "", "success" or "failure".
	Conclusion string
	RunnerID   int64
	RunnerName string
}

type jobState struct {
	Job
	// announced is set once the queued delivery returned. Pairing ignores a
	// job that is not announced yet, so in_progress never overtakes queued.
	announced   bool
	createdAt   time.Time
	startedAt   time.Time
	completedAt time.Time
}

const (
	jobQueued     = "queued"
	jobInProgress = "in_progress"
	jobCompleted  = "completed"
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
	js := &jobState{Job: Job{
		RunID:  s.nextIDLocked(),
		Repo:   repo,
		Labels: append([]string(nil), labels...),
		Status: jobQueued,
	}, createdAt: s.clock.Now()}
	js.ID = s.nextIDLocked()
	s.jobs[js.ID] = js
	s.runs[js.RunID] = js.ID
	snap := js.snapshot()
	s.mu.Unlock()

	s.deliver(jobQueued, snap)

	s.mu.Lock()
	js.announced = true
	s.mu.Unlock()
	s.wake()
	return snap, nil
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
	for _, j := range s.jobs {
		if j.Repo != scope.Repo || (status != "" && j.Status != status) {
			continue
		}
		runs = append(runs, &github.WorkflowRun{
			ID:         new(j.RunID),
			Name:       new("run"),
			Status:     new(j.Status),
			Conclusion: optString(j.Conclusion),
			Repository: &github.Repository{FullName: new(j.Repo)},
			CreatedAt:  &github.Timestamp{Time: j.createdAt},
		})
	}
	s.mu.Unlock()
	sort.Slice(runs, func(i, k int) bool { return runs[i].GetID() < runs[k].GetID() })
	lo, hi, link := paginate(r, len(runs))
	setLink(w, link)
	writeJSON(w, http.StatusOK, &github.WorkflowRuns{TotalCount: new(len(runs)), WorkflowRuns: runs[lo:hi]})
}

func (s *Server) handleRunJobs(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	runID, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	s.mu.Lock()
	js, found := s.jobs[s.runs[runID]]
	var out *github.WorkflowJob
	if found && js.Repo == scope.Repo {
		out = toGitHubJob(js)
	}
	s.mu.Unlock()
	if err != nil || out == nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, &github.Jobs{TotalCount: new(1), Jobs: []*github.WorkflowJob{out}})
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

// wake tells the lifecycle scheduler that state changed. Task 6 gives it a
// body; until then nothing waits on it.
func (s *Server) wake() {}
