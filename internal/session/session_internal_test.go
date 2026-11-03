package session

import "testing"

func TestMailboxKeepsSeenAndBusy(t *testing.T) {
	s := newSession("abcdefghijkl", &pool{}, "r")
	s.post(mailbox{polled: true, listed: true, online: true, busy: true})
	s.post(mailbox{polled: true, absent: true})
	m := s.drain()
	if !m.listed || !m.online || !m.busy || !m.absent {
		t.Fatalf("drained %+v, want listed, online, busy and absent", m)
	}
	if again := s.drain(); again != (mailbox{}) {
		t.Fatalf("second drain = %+v, want empty", again)
	}
	s.post(mailbox{polled: true, absent: true})
	s.post(mailbox{polled: true, listed: true})
	if m := s.drain(); m.absent {
		t.Fatal("absent survived a later poll that listed the runner")
	}
}

func TestJobEventKeepsAbsent(t *testing.T) {
	s := newSession("abcdefghijkl", &pool{}, "r")
	s.post(mailbox{polled: true, absent: true})
	s.post(mailbox{busy: true})
	s.post(mailbox{completed: true})
	if m := s.drain(); !m.absent {
		t.Fatal("a job event erased an unread absence")
	}
}
