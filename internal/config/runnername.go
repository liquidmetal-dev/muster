package config

// SessionIDLen is the length of a session id, which ends every runner name.
const SessionIDLen = 12

// GitHub rejects longer runner names.
const maxRunnerName = 64

// RunnerNamePrefix is the part of a runner name before the session id.
func RunnerNamePrefix(namespace, pool string) string {
	return "muster-" + namespace + "-" + pool + "-"
}
