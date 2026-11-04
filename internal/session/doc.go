// Package session runs the muster session state machine and keeps the
// in-memory registry of live sessions.
//
// A session is one claimed VM, one just-in-time runner and at most one job:
//
//	PENDING ─▶ CLAIMING ─▶ STARTING ─▶ RUNNING ─▶ ENDED ─▶ RELEASED
//	              └──────────┴───────────┴──▶ FAILED ─▶ RELEASED
//
// The registry implements dispatch.Sessions. It owns one runner poll per
// GitHub scope and one VM event subscription per pool, and it passes what
// they observe to each session goroutine.
package session
