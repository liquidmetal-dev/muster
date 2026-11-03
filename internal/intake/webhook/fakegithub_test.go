package webhook_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/fakegithub"
	"github.com/liquidmetal-dev/muster/internal/intake/webhook"
)

func TestFakeGitHubDeliveriesAndRedelivery(t *testing.T) {
	d := &fakeDemand{}
	srv := httptest.NewServer(newHandler(t, webhook.Config{Demand: d}))
	t.Cleanup(srv.Close)
	f := fakegithub.New(fakegithub.Config{WebhookURL: srv.URL, WebhookSecret: secret})
	t.Cleanup(f.Close)
	f.AddOrg("acme")
	f.AddRepo("acme/app")

	if _, err := f.QueueJob("acme/app", labels); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	first := f.Deliveries()[0]
	if first.Status != http.StatusOK || len(d.queued) != 1 {
		t.Fatalf("status = %d, queued = %d, want 200 and 1", first.Status, len(d.queued))
	}

	if err := f.Redeliver(first.ID); err != nil {
		t.Fatalf("Redeliver: %v", err)
	}
	if got := f.Deliveries()[1].Status; got != http.StatusOK || len(d.queued) != 1 {
		t.Fatalf("redelivery: status = %d, queued = %d, want 200 and 1", got, len(d.queued))
	}

	f.SetWebhook(srv.URL, "wrong")
	if _, err := f.QueueJob("acme/app", labels); err != nil {
		t.Fatalf("QueueJob: %v", err)
	}
	if got := f.Deliveries()[2].Status; got != http.StatusUnauthorized || len(d.queued) != 1 {
		t.Fatalf("wrong secret: status = %d, queued = %d, want 401 and 1", got, len(d.queued))
	}
}
