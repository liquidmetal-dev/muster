package fakegithub_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

const hookSecret = "s3cret"

type hookReq struct {
	req  *http.Request
	body []byte
}

// hook runs a webhook receiver that records every delivery and answers 200.
func hook(t *testing.T) (string, <-chan hookReq) {
	t.Helper()
	ch := make(chan hookReq, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- hookReq{req: r, body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ch
}

func withHook(url string) func(*fakegithub.Config) {
	return func(c *fakegithub.Config) {
		c.WebhookURL = url
		c.WebhookSecret = hookSecret
	}
}

func recv(t *testing.T, ch <-chan hookReq) hookReq {
	t.Helper()
	select {
	case h := <-ch:
		return h
	case <-time.After(2 * time.Second):
		t.Fatal("no webhook delivery within 2s")
		return hookReq{}
	}
}

func parseEvent(t *testing.T, h hookReq, secret string) *github.WorkflowJobEvent {
	t.Helper()
	h.req.Body = io.NopCloser(bytes.NewReader(h.body))
	payload, err := github.ValidatePayload(h.req, []byte(secret))
	if err != nil {
		t.Fatalf("ValidatePayload: %v", err)
	}
	ev, err := github.ParseWebHook(github.WebHookType(h.req), payload)
	if err != nil {
		t.Fatalf("ParseWebHook: %v", err)
	}
	wj, ok := ev.(*github.WorkflowJobEvent)
	if !ok {
		t.Fatalf("event type %T", ev)
	}
	return wj
}

func TestQueueJobDeliversSignedQueued(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	job := queue(t, e, orgRepo, "self-hosted", "linux")
	h := recv(t, ch)
	if h.req.Header.Get("X-GitHub-Event") != "workflow_job" || h.req.Header.Get("X-GitHub-Delivery") == "" {
		t.Fatalf("headers %v", h.req.Header)
	}
	ev := parseEvent(t, h, hookSecret)
	if ev.GetAction() != "queued" || ev.GetWorkflowJob().GetID() != job.ID {
		t.Fatalf("event %+v", ev)
	}
	if got := ev.GetWorkflowJob().Labels; len(got) != 2 || got[0] != "self-hosted" {
		t.Fatalf("labels %v", got)
	}
	if ev.GetRepo().GetFullName() != orgRepo || ev.GetOrg().GetLogin() != org || ev.GetInstallation().GetID() != installationID {
		t.Fatalf("repo %+v org %+v installation %+v", ev.GetRepo(), ev.GetOrg(), ev.GetInstallation())
	}
}

func TestDeliveryRecorded(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	job := queue(t, e, orgRepo, "x")
	recv(t, ch)
	ds := e.fake.Deliveries()
	if len(ds) != 1 {
		t.Fatalf("%d deliveries", len(ds))
	}
	d := ds[0]
	if d.Action != "queued" || d.JobID != job.ID || d.Status != 200 || d.Err != nil || d.Header.Get("X-Hub-Signature-256") == "" {
		t.Fatalf("delivery %+v", d)
	}
}

func TestDeliveryWithoutWebhookRecordsErrNoWebhook(t *testing.T) {
	e := start(t)
	seed(e)
	queue(t, e, orgRepo, "x")
	ds := e.fake.Deliveries()
	if len(ds) != 1 || !errors.Is(ds[0].Err, fakegithub.ErrNoWebhook) || ds[0].Status != 0 {
		t.Fatalf("deliveries %+v", ds)
	}
}

func TestDeliveryToDeadEndpointRecordsError(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + lis.Addr().String()
	_ = lis.Close()
	e := start(t, withHook(dead))
	seed(e)
	queue(t, e, orgRepo, "x")
	ds := e.fake.Deliveries()
	if len(ds) != 1 || ds[0].Err == nil || ds[0].Status != 0 {
		t.Fatalf("deliveries %+v", ds)
	}
	if len(e.fake.Jobs()) != 1 {
		t.Fatal("job not stored")
	}
}

func TestDeliveryWrongSecretFailsValidation(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	queue(t, e, orgRepo, "x")
	h := recv(t, ch)
	h.req.Body = io.NopCloser(bytes.NewReader(h.body))
	if _, err := github.ValidatePayload(h.req, []byte("other")); err == nil {
		t.Fatal("wrong secret validated")
	}
}

func TestRedeliverRepeatsBodyAndID(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	queue(t, e, orgRepo, "x")
	first := recv(t, ch)
	id := e.fake.Deliveries()[0].ID
	if err := e.fake.Redeliver(id); err != nil {
		t.Fatalf("Redeliver: %v", err)
	}
	second := recv(t, ch)
	if first.req.Header.Get("X-GitHub-Delivery") != second.req.Header.Get("X-GitHub-Delivery") {
		t.Fatal("delivery ids differ")
	}
	if !bytes.Equal(first.body, second.body) {
		t.Fatal("bodies differ")
	}
	if len(e.fake.Deliveries()) != 2 {
		t.Fatalf("%d deliveries recorded", len(e.fake.Deliveries()))
	}
	if err := e.fake.Redeliver("nope"); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestSetWebhookTakesEffect(t *testing.T) {
	url, ch := hook(t)
	e := start(t)
	seed(e)
	e.fake.SetWebhook(url, hookSecret)
	queue(t, e, orgRepo, "x")
	parseEvent(t, recv(t, ch), hookSecret)
}

func TestRepoWithoutOrgOmitsOrganization(t *testing.T) {
	url, ch := hook(t)
	e := start(t, withHook(url))
	seed(e)
	queue(t, e, privateRepo, "x")
	ev := parseEvent(t, recv(t, ch), hookSecret)
	if ev.Org != nil {
		t.Fatalf("org %+v, want nil", ev.Org)
	}
	if ev.GetRepo().GetOwner().GetLogin() != "someone" {
		t.Fatalf("owner %+v", ev.GetRepo().GetOwner())
	}
}
