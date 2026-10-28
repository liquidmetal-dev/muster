package fakegithub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/go-github/v93/github"
)

// ErrNoWebhook is recorded on a delivery when no webhook URL is configured.
var ErrNoWebhook = errors.New("fakegithub: no webhook URL configured")

const webhookTimeout = 10 * time.Second

// Delivery is one attempted webhook delivery. Every lifecycle event produces
// one, whether or not a webhook URL is configured, so Deliveries doubles as
// the event log.
type Delivery struct {
	// ID is the X-GitHub-Delivery value, a UUID.
	ID string
	// Event is always "workflow_job".
	Event string
	// Action is "queued", "in_progress" or "completed".
	Action string
	JobID  int64
	// Header holds the headers sent, including X-Hub-Signature-256.
	Header http.Header
	// Body is the JSON sent.
	Body []byte
	// Status is the response code; 0 when not sent or on a transport error.
	Status int
	// Err is ErrNoWebhook, a transport error, or nil.
	Err error
}

// Deliveries returns a copy of every delivery record in order.
func (s *Server) Deliveries() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Delivery, len(s.deliveries))
	copy(out, s.deliveries)
	return out
}

// SetWebhook replaces the webhook URL and secret. It is safe while serving.
func (s *Server) SetWebhook(url, secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.WebhookURL = url
	s.cfg.WebhookSecret = secret
}

// Redeliver sends the delivery with the given id again, with the same body
// and headers, and appends a new record.
func (s *Server) Redeliver(deliveryID string) error {
	s.mu.Lock()
	var found *Delivery
	for i := range s.deliveries {
		if s.deliveries[i].ID == deliveryID {
			found = &s.deliveries[i]
			break
		}
	}
	if found == nil {
		s.mu.Unlock()
		return fmt.Errorf("fakegithub: unknown delivery %q", deliveryID)
	}
	d := *found
	d.Header = found.Header.Clone()
	d.Body = append([]byte(nil), found.Body...)
	url := s.cfg.WebhookURL
	s.mu.Unlock()
	s.send(url, d)
	return nil
}

// deliver builds, signs, sends and records a workflow_job event for job.
func (s *Server) deliver(action string, job Job) Delivery {
	s.mu.Lock()
	payload := s.payloadLocked(action, job)
	url, secret := s.cfg.WebhookURL, s.cfg.WebhookSecret
	s.mu.Unlock()

	body, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "GitHub-Hookshot/fakegithub")
	h.Set("X-GitHub-Event", "workflow_job")
	h.Set("X-GitHub-Delivery", newUUID())
	h.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	h.Set("X-GitHub-Hook-Installation-Target-ID", strconv.FormatInt(s.cfg.AppID, 10))
	h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	d := Delivery{ID: h.Get("X-GitHub-Delivery"), Event: "workflow_job", Action: action, JobID: job.ID, Header: h, Body: body}
	return s.send(url, d)
}

// send posts d to url, fills Status and Err, records it and returns it.
func (s *Server) send(url string, d Delivery) Delivery {
	d.Status, d.Err = 0, nil
	if url == "" {
		d.Err = ErrNoWebhook
	} else {
		d.Status, d.Err = post(url, d)
	}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, d)
	s.mu.Unlock()
	return d
}

func post(url string, d Delivery) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(d.Body))
	if err != nil {
		return 0, err
	}
	req.Header = d.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// payloadLocked builds the workflow_job event for job as GitHub sends it.
func (s *Server) payloadLocked(action string, job Job) *github.WorkflowJobEvent {
	js := s.jobs[job.ID]
	ev := &github.WorkflowJobEvent{
		Action:       new(action),
		WorkflowJob:  toGitHubJob(js),
		Installation: &github.Installation{ID: new(s.cfg.InstallationID)},
	}
	if repo, ok := s.repos[job.Repo]; ok {
		ev.Repo = &github.Repository{
			ID:       new(repo.id),
			Name:     new(repo.name),
			FullName: new(repo.fullName),
			Owner:    &github.User{Login: new(repo.owner)},
		}
		if _, isOrg := s.orgs[repo.owner]; isOrg {
			ev.Org = &github.Organization{Login: new(repo.owner)}
		}
	}
	return ev
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
