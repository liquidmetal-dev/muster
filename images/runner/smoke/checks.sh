#!/usr/bin/env bash
# Runs inside a booted runner image as root and prints one "SMOKE:" line per check.
# SMOKE_NETWORK=1 adds the network checks.
set -uo pipefail

pass=0
fail=0

result() {
  if [ "$2" = 0 ]; then
    echo "SMOKE: PASS $1"
    pass=$((pass + 1))
  else
    echo "SMOKE: FAIL $1: $3"
    fail=$((fail + 1))
  fi
}

# In vm mode smoke.service is itself part of boot, so the system cannot reach "running" before
# it ends; the unit's ordering does the waiting instead.
state=running
[ "${SMOKE_MODE:-container}" = vm ] || for _ in $(seq 180); do
  state="$(systemctl is-system-running 2>/dev/null)"
  [ "$state" = running ] || [ "$state" = degraded ] && break
  sleep 1
done
if [ "$state" = degraded ]; then
  systemctl --failed --no-pager --plain
fi
if [ "$state" != running ] && [ "$state" != degraded ]; then
  result boot 1 "system state is '$state' after 180 s"
fi

systemctl is-active --quiet guest-agent
result guest_agent $? "guest-agent is $(systemctl is-active guest-agent)"

cgroup="$(docker info -f '{{.CgroupVersion}}' 2>&1)"
systemctl is-active --quiet docker && [ "$cgroup" = 2 ]
result dockerd $? "docker is $(systemctl is-active docker), cgroup version '$cgroup'"

systemctl is-active --quiet containerd
result containerd $? "containerd is $(systemctl is-active containerd)"

out="$(/usr/local/bin/muster-runner --check 2>&1)"
result muster_runner_check $? "$(echo "$out" | grep ^fail | tr '\n' ' ')"

if [ "${SMOKE_NETWORK:-0}" = 1 ]; then
  if ip -4 -o addr show scope global dynamic | grep -q inet; then
    result dhcp 0
  else
    result dhcp 1 "no dynamic IPv4 address: $(ip -4 -o addr show scope global | tr '\n' ' ')"
  fi

  if [ -s /etc/machine-id ]; then result machine_id 0; else result machine_id 1 "/etc/machine-id is empty"; fi

  getent hosts api.github.com >/dev/null
  result dns $? "api.github.com does not resolve"

  out="$(curl -fsS -o /dev/null https://api.github.com 2>&1)"
  result https $? "$out"
fi

echo "SMOKE: DONE pass=$pass fail=$fail"
[ "$fail" = 0 ]
