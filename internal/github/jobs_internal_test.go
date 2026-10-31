package github

import (
	"testing"

	gogithub "github.com/google/go-github/v93/github"
)

func TestQueuedOnly(t *testing.T) {
	jobs := []*gogithub.WorkflowJob{
		{ID: new(int64(1)), Status: new("queued"), Labels: []string{"a"}},
		{ID: new(int64(2)), Status: new("in_progress")},
		{ID: new(int64(3)), Status: new("completed")},
		{ID: new(int64(4)), Status: new("queued"), Labels: []string{"b"}},
	}
	got := queuedOnly("acme/app", 9, jobs)
	if len(got) != 2 || got[0].JobID != 1 || got[1].JobID != 4 || got[0].RunID != 9 || got[1].Repository != "acme/app" || got[1].Labels[0] != "b" {
		t.Fatalf("queuedOnly = %+v", got)
	}
}
