// Package demand is the registry of queued GitHub jobs that both intakes
// feed (design §5). It matches each job to a pool once, when it first sees
// the job (requirement POOL-5), counts it once however many intakes report
// it (INTAKE-5), and computes desired(pool) as the number of open records
// for that pool.
//
// A record closes when the job starts or completes, when a poll of its
// repository that was listed after muster last saw the job no longer
// includes it, or MaxAge after it was queued. Closed ids are kept as
// tombstones for MaxAge, so a late or repeated event cannot reopen them.
package demand
