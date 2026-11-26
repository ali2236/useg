#!/bin/sh
# Nightly copy, 14 kept. Backing up SQLite while it is open requires .backup.
set -eu
dir=${USEG_BACKUP_DIR:-/var/lib/useg/backups}
mkdir -p "$dir"
sqlite3 /var/lib/useg/useg.db ".backup '$dir/useg-$(date +%Y%m%d-%H%M).db'"
ls -1t "$dir"/useg-*.db | tail -n +15 | xargs -r rm --
