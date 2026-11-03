package server

import (
	"net/http"
	"testing"

	"github.com/liquidmetal-dev/muster/internal/config"
)

func TestTimeoutsSet(t *testing.T) {
	s, err := New(Config{Listen: config.Listen{}, Webhook: http.NotFoundHandler()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.http.ReadHeaderTimeout != ReadHeaderTimeout || s.http.ReadTimeout != ReadTimeout ||
		s.http.WriteTimeout != WriteTimeout || s.http.IdleTimeout != IdleTimeout {
		t.Fatalf("timeouts = %v %v %v %v, want %v %v %v %v",
			s.http.ReadHeaderTimeout, s.http.ReadTimeout, s.http.WriteTimeout, s.http.IdleTimeout,
			ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout)
	}
}
