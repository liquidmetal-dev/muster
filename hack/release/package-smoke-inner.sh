#!/usr/bin/env bash
# Usage: package-smoke-inner.sh <version>   (as root, in a throwaway container)
set -euo pipefail

version="${1:?usage: package-smoke-inner.sh <version>}"

fail() {
	echo "package smoke: FAIL: $*" >&2
	exit 1
}

one() {
	local matches
	# shellcheck disable=SC2206
	matches=($1)
	[ "${#matches[@]}" -eq 1 ] && [ -e "${matches[0]}" ] || fail "want one file for $1, got: ${matches[*]}"
	echo "${matches[0]}"
}

if command -v apt-get >/dev/null; then
	export DEBIAN_FRONTEND=noninteractive
	arch="$(dpkg --print-architecture)"
	musterd_pkg="$(one "/dist/musterd_*_${arch}.deb")"
	musterctl_pkg="$(one "/dist/musterctl_*_${arch}.deb")"
	apt-get update -qq
	apt-get install -y -qq --no-install-recommends systemd openssl >/dev/null
	install_pkgs() { apt-get install -y -qq "$@" "$musterd_pkg" "$musterctl_pkg" >/dev/null; }
	install_pkgs
	reinstall_pkgs() { install_pkgs --reinstall; }
else
	arch="$(uname -m)"
	musterd_pkg="$(one "/dist/musterd-*.${arch}.rpm")"
	musterctl_pkg="$(one "/dist/musterctl-*.${arch}.rpm")"
	dnf install -y -q systemd openssl util-linux >/dev/null
	dnf install -y -q "$musterd_pkg" "$musterctl_pkg" >/dev/null
	reinstall_pkgs() { dnf reinstall -y -q "$musterd_pkg" "$musterctl_pkg" >/dev/null; }
fi

IFS=: read -r _ _ uid _ _ _ shell < <(getent passwd muster) || fail "no muster user"
[ "$shell" = /usr/sbin/nologin ] || fail "muster shell is $shell"
[ "$uid" -lt 1000 ] || fail "muster uid $uid is not a system uid"

dir_stat="$(stat -c '%a %U %G' /etc/muster)"
[ "$dir_stat" = "750 root muster" ] || fail "/etc/muster is $dir_stat"
[ ! -e /etc/muster/musterd.json ] || fail "the package installed /etc/muster/musterd.json"
[ -f /usr/share/muster/musterd.json.example ] || fail "no example configuration"
[ -f /usr/lib/sysusers.d/muster.conf ] || fail "no sysusers file"

systemd-analyze verify /usr/lib/systemd/system/musterd.service || fail "systemd-analyze verify"

case "$(musterd -version)" in "musterd v$version "*) ;; *) fail "musterd -version: $(musterd -version)" ;; esac
case "$(musterctl version)" in "musterctl v$version "*) ;; *) fail "musterctl version: $(musterctl version)" ;; esac

sed -e 's|"private_key_env": "MUSTER_APP_KEY"|"private_key_file": "/etc/muster/app.pem"|' \
	-e 's|"webhook_secret_env": "MUSTER_WEBHOOK_SECRET"|"webhook_secret_file": "/etc/muster/webhook.secret"|' \
	/src/internal/config/testdata/valid.json >/etc/muster/musterd.json
openssl genrsa -traditional -out /etc/muster/app.pem 2048 2>/dev/null
printf 'wh' >/etc/muster/webhook.secret
chown root:muster /etc/muster/musterd.json /etc/muster/app.pem /etc/muster/webhook.secret
chmod 0640 /etc/muster/musterd.json /etc/muster/app.pem /etc/muster/webhook.secret

want="musterd: configuration loaded from /etc/muster/musterd.json (1 pools)"
got="$(setpriv --reuid=muster --regid=muster --init-groups /usr/bin/musterd)" || fail "musterd as muster: $got"
[ "$got" = "$want" ] || fail "musterd as muster printed: $got"

before="$(stat -c '%a %U %G %Y' /etc/muster/musterd.json) $(sha256sum </etc/muster/musterd.json)"
reinstall_pkgs
after="$(stat -c '%a %U %G %Y' /etc/muster/musterd.json) $(sha256sum </etc/muster/musterd.json)"
[ "$before" = "$after" ] || fail "reinstall changed musterd.json: $before -> $after"
dir_stat="$(stat -c '%a %U %G' /etc/muster)"
[ "$dir_stat" = "750 root muster" ] || fail "/etc/muster is $dir_stat after reinstall"

# shellcheck source=/dev/null
. /etc/os-release
echo "package smoke: OK on $ID $VERSION_ID"
