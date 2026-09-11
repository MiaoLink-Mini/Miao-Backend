#!/bin/sh
set -eu
umask 077
cd /opt/weagent
name="backups/weagent-$(date -u +%Y%m%dT%H%M%SZ).dump"
trap 'rm -f "$name.tmp"' EXIT
docker exec weagent-db pg_dump -U weagent_owner -d weagent -Fc > "$name.tmp"
test -s "$name.tmp"
docker exec -i weagent-db pg_restore --list < "$name.tmp" >/dev/null
mv "$name.tmp" "$name"
printf 'Verified database backup: %s\n' "$name"
