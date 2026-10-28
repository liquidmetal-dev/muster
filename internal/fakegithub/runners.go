package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-github/v93/github"
)

// Scope is where a runner is registered: exactly one field is set.
type Scope struct {
	// Org is set for organization scope.
	Org string
	// Repo is "owner/name", set for repository scope.
	Repo string
}

// Runner is a snapshot of one registered runner.
type Runner struct {
	ID            int64
	Name          string
	Scope         Scope
	RunnerGroupID int64
	Labels        []string
	// Status is "offline" or "online".
	Status string
	Busy   bool
	// JobID is the job in progress, 0 when idle.
	JobID int64
	// Finished is set once the runner ran its one job. It is never paired
	// again and is removed after its deregister delay.
	Finished bool
}

type orgState struct {
	login  string
	groups []groupState // ordered by id
}

type groupState struct {
	id   int64
	name string
}

type repoState struct {
	id       int64
	fullName string
	owner    string
	name     string
}

type runnerState struct {
	Runner
}

const (
	defaultGroupID   = 1
	defaultGroupName = "Default"
	statusOffline    = "offline"
	statusOnline     = "online"
)

// AddOrg seeds an organization with a Default runner group of id 1. It is
// idempotent.
func (s *Server) AddOrg(login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[login]; ok {
		return
	}
	s.orgs[login] = &orgState{login: login, groups: []groupState{{id: defaultGroupID, name: defaultGroupName}}}
}

// AddRepo seeds a repository named "owner/name". The owner need not be a
// seeded organization. It is idempotent and panics on a name without "/".
func (s *Server) AddRepo(fullName string) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		panic(fmt.Sprintf("fakegithub: AddRepo(%q): want owner/name", fullName))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.repos[fullName]; ok {
		return
	}
	s.repos[fullName] = &repoState{id: int64(len(s.repos) + 1), fullName: fullName, owner: owner, name: name}
}

// AddRunnerGroup adds a runner group to a seeded organization and returns
// its id. It fails when the organization is unknown or the name exists.
func (s *Server) AddRunnerGroup(org, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[org]
	if !ok {
		return 0, fmt.Errorf("fakegithub: unknown org %q", org)
	}
	for _, g := range o.groups {
		if g.name == name {
			return 0, fmt.Errorf("fakegithub: runner group %q exists in %q", name, org)
		}
	}
	id := o.groups[len(o.groups)-1].id + 1
	o.groups = append(o.groups, groupState{id: id, name: name})
	return id, nil
}

// Runners returns a snapshot of every registered runner, ordered by id.
func (s *Server) Runners() []Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runnersLocked(func(*runnerState) bool { return true })
}

func (s *Server) runnersLocked(keep func(*runnerState) bool) []Runner {
	out := make([]Runner, 0, len(s.runners))
	for _, r := range s.runners {
		if keep(r) {
			snap := r.Runner
			snap.Labels = append([]string(nil), r.Labels...)
			out = append(out, snap)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Server) nextIDLocked() int64 {
	s.nextID++
	return s.nextID
}

// scopeFromRequest resolves the org or repo path values to a Scope. ok is
// false when the scope is not seeded.
func (s *Server) scopeFromRequest(r *http.Request) (Scope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if org := r.PathValue("org"); org != "" {
		_, ok := s.orgs[org]
		return Scope{Org: org}, ok
	}
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	_, ok := s.repos[full]
	return Scope{Repo: full}, ok
}

func (s *Server) registerRunnerRoutes() {
	s.route(PatternRunnerGroups, authAPI, s.handleRunnerGroups)
	s.route(PatternOrgJITConfig, authAPI, s.handleJITConfig)
	s.route(PatternRepoJITConfig, authAPI, s.handleJITConfig)
	s.route(PatternOrgRunners, authAPI, s.handleListRunners)
	s.route(PatternRepoRunners, authAPI, s.handleListRunners)
	s.route(PatternOrgRunnerDel, authAPI, s.handleDeleteRunner)
	s.route(PatternRepoRunnerDel, authAPI, s.handleDeleteRunner)
}

func (s *Server) handleRunnerGroups(w http.ResponseWriter, r *http.Request, _ authInfo) {
	s.mu.Lock()
	o, ok := s.orgs[r.PathValue("org")]
	var groups []*github.RunnerGroup
	if ok {
		for _, g := range o.groups {
			groups = append(groups, &github.RunnerGroup{
				ID: new(g.id), Name: new(g.name), Default: new(g.id == defaultGroupID), Visibility: new("all"),
			})
		}
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	lo, hi, link := paginate(r, len(groups))
	setLink(w, link)
	writeJSON(w, http.StatusOK, &github.RunnerGroups{TotalCount: len(groups), RunnerGroups: groups[lo:hi]})
}

func (s *Server) handleJITConfig(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	var req github.CreateJITConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || len(req.Labels) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.groupExistsLocked(scope, req.RunnerGroupID) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	for _, existing := range s.runners {
		if existing.Scope == scope && existing.Name == req.Name {
			writeError(w, http.StatusConflict, "Already exists")
			return
		}
	}
	rs := &runnerState{Runner: Runner{
		ID:            s.nextIDLocked(),
		Name:          req.Name,
		Scope:         scope,
		RunnerGroupID: req.RunnerGroupID,
		Labels:        append([]string(nil), req.Labels...),
		Status:        statusOffline,
	}}
	s.runners[rs.ID] = rs
	blob := JITConfig{RunnerID: rs.ID, RunnerName: rs.Name, SleepSeconds: 5, ExitCode: 0}
	writeJSON(w, http.StatusCreated, &github.JITRunnerConfig{
		Runner:           toGitHubRunner(rs),
		EncodedJITConfig: new(blob.Encode()),
	})
}

func (s *Server) groupExistsLocked(scope Scope, id int64) bool {
	if scope.Org == "" {
		return id == defaultGroupID
	}
	for _, g := range s.orgs[scope.Org].groups {
		if g.id == id {
			return true
		}
	}
	return false
}

func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	name := r.URL.Query().Get("name")
	s.mu.Lock()
	snaps := s.runnersLocked(func(rs *runnerState) bool {
		return rs.Scope == scope && (name == "" || rs.Name == name)
	})
	s.mu.Unlock()
	lo, hi, link := paginate(r, len(snaps))
	setLink(w, link)
	out := make([]*github.Runner, 0, hi-lo)
	for _, snap := range snaps[lo:hi] {
		out = append(out, toGitHubRunner(&runnerState{Runner: snap}))
	}
	writeJSON(w, http.StatusOK, &github.Runners{TotalCount: len(snaps), Runners: out})
}

func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request, _ authInfo) {
	scope, ok := s.scopeFromRequest(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, found := s.runners[id]
	if err != nil || !found || rs.Scope != scope {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	if rs.Busy {
		writeError(w, http.StatusUnprocessableEntity, "Runner is busy")
		return
	}
	delete(s.runners, id)
	w.WriteHeader(http.StatusNoContent)
}

func toGitHubRunner(rs *runnerState) *github.Runner {
	labels := make([]*github.RunnerLabels, 0, len(rs.Labels))
	for _, l := range rs.Labels {
		labels = append(labels, &github.RunnerLabels{Name: new(l), Type: new("custom")})
	}
	return &github.Runner{
		ID:            new(rs.ID),
		RunnerGroupID: new(rs.RunnerGroupID),
		Name:          new(rs.Name),
		OS:            new("linux"),
		Status:        new(rs.Status),
		Busy:          new(rs.Busy),
		Labels:        labels,
		Ephemeral:     new(true),
	}
}
