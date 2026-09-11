#!/bin/sh
set -eu
umask 077
cd /opt/weagent
mkdir -p backups
name="backups/weagent-$(date -u +%Y%m%dT%H%M%SZ).dump"
trap 'rm -f "$name.tmp"' EXIT
docker compose --env-file private/release.env -f current/compose.yaml exec -T db pg_dump -U weagent_owner -d weagent -Fc > "$name.tmp"
test -s "$name.tmp"
docker compose --env-file private/release.env -f current/compose.yaml exec -T db pg_restore --list < "$name.tmp" >/dev/null
mv "$name.tmp" "$name"
# Retention deletion is intentionally manual until off-host backups are configured.
printf 'Verified database backup: %s\n' "$name"
