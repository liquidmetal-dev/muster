#!/bin/sh
# Runs on every install and upgrade, for dpkg and rpm alike, so every step is
# idempotent and nothing inside /etc/muster is touched.
set -e

systemd-sysusers /usr/lib/sysusers.d/muster.conf
chgrp muster /etc/muster
chmod 0750 /etc/muster

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
fi
