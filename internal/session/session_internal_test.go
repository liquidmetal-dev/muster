package session

import "testing"

func TestMailboxKeepsSeenAndBusy(t *testing.T) {
	s := newSession("abcdefghijkl", &pool{}, "r")
	s.post(mailbox{listed: true, online: true, busy: true})
	s.post(mailbox{absent: true})
	m := s.drain()
	if !m.listed || !m.online || !m.busy || !m.absent {
		t.Fatalf("drained %+v, want listed, online, busy and absent", m)
	}
	if again := s.drain(); again != (mailbox{}) {
		t.Fatalf("second drain = %+v, want empty", again)
	}
	s.post(mailbox{absent: true})
	s.post(mailbox{listed: true})
	if m := s.drain(); m.absent {
		t.Fatal("absent survived a later poll that listed the runner")
	}
}
