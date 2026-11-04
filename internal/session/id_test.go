package session

import (
	"crypto/rand"
	"errors"
	"regexp"
	"testing"
	"testing/iotest"
)

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{12}$`)
	a, err := newID(rand.Reader)
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	b, err := newID(rand.Reader)
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("ids %q, %q do not match %v", a, b, re)
	}
	if a == b {
		t.Fatalf("two ids are equal: %q", a)
	}
	boom := errors.New("boom")
	if _, err := newID(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("newID(error reader) = %v, want %v", err, boom)
	}
}

func TestRunnerName(t *testing.T) {
	if got := runnerName("muster", "linux", "abcdefghijkl"); got != "muster-muster-linux-abcdefghijkl" {
		t.Fatalf("runnerName = %q", got)
	}
}
