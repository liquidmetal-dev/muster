# Muster

Muster turns queued GitHub Actions jobs into one-shot microVM runners. These terms name the
things muster tracks and the events that move them.

## Sessions

**Session**:
Muster's unit of work: one claimed VM, one just-in-time runner, at most one job.
_Avoid_: Lease, job, runner (each names only one part of a session)

**Session id**:
The short random identifier of a session. It is both the claim's request id and the suffix
of the runner name, so a lease and a runner that share it belong to the same session.
_Avoid_: Short id, request id

**Runner name**:
The GitHub runner name of a session: `muster-<namespace>-<pool>-<session id>`.

**Active session**:
A session in PENDING, CLAIMING, STARTING or RUNNING.

**Busy**:
A session whose runner GitHub has reported running a job at least once. A busy session
stays busy until it ends.
_Avoid_: In use, assigned

**End signal**:
An observation that a session's runner is finished: exec exit, job completed plus grace,
runner absent from GitHub, idle timeout, or maximum session duration. The first end signal
ends the session.

**Adopted session**:
A session muster finds in flight at startup and resumes in RUNNING without an exec stream.
