#!/bin/bash
# Run by useg-update.service when the admin panel asks for an update. install.sh
# fills in @SRC@ with the checkout it was installed from.
set -eu
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
src=@SRC@
req=/var/lib/useg/update.request
status=/var/lib/useg/update.status

# One update at a time. A click during a run is already covered by that run.
exec 9>/var/lib/useg/update.lock
if ! flock -n 9; then
  rm -f "$req"
  exit 0
fi
rm -f "$req"
printf 'running %s\n' "$(date +%s)" > "$status"
if GIT_TERMINAL_PROMPT=0 make -C "$src" update; then
  printf 'ok %s\n' "$(date +%s)" > "$status"
else
  printf 'failed %s\n' "$(date +%s)" > "$status"
  exit 1
fi
