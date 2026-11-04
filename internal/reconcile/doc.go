// Package reconcile brings musterd back in step with the outside world at
// startup and lets it stop without losing in-flight jobs (REL-2, REL-6).
//
// Run pairs each battery lease in the namespace with the GitHub runner
// named for the lease's pool and session id. A pair is adopted as a
// RUNNING session, a lease with no runner is released, and a runner with
// no lease is deleted. Run must finish before any intake starts.
//
// Shutdown stops new sessions, waits a bounded time for busy sessions to
// end, then stops the registry and leaves the rest for the next Run.
package reconcile
