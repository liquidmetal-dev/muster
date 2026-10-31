// Package dispatch is the level-triggered loop of design §5. On each pass
// it creates, for every pool in configuration order, desired(pool) minus
// the active sessions whose runner has not been seen busy, without going
// above the pool's max_concurrent active sessions (requirements SESSION-0,
// POOL-6). It never destroys a session when demand drops. While drained it
// creates nothing (SESSION-11).
package dispatch
