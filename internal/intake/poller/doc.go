// Package poller is the polling intake (design §5). Each poll pass lists the
// queued jobs of every configured repository (requirement INTAKE-5) and
// merges each listing into the demand registry, which counts a job once
// whichever intake reports it and closes a job that a newer listing omits.
//
// Repositories are listed one after another, and a pass never overlaps the
// next. A repository whose listing fails is not merged, so its open records
// stay open. A rate-limit error ends the pass. A disabled poller makes no
// GitHub calls (INTAKE-6). See docs/adr/2026-11-03-polling-intake.md.
package poller
