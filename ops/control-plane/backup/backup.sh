#!/bin/sh
set -eu

renderops_dir=${RENDEROPS_DIR:-/opt/renderops}
backup_dir="$renderops_dir/backups/daily"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)

umask 077
mkdir -p "$backup_dir"
cd "$renderops_dir"

docker compose exec -T postgres \
	pg_dump --username=renderops --dbname=renderops --format=custom --no-owner --no-acl \
	> "$backup_dir/renderops-$timestamp.dump"

find "$backup_dir" -type f -name 'renderops-*.dump' -mtime +7 -delete
