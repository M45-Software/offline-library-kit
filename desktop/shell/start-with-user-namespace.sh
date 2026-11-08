#!/bin/sh
# Optional workaround on hosts that restrict Electron's unprivileged sandbox.
set -eu
app_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
app_uid=$(id -u)
app_gid=$(id -g)
if [ "$app_uid" -eq 0 ]; then
  printf '%s\n' 'Run this launcher as a regular user.' >&2
  exit 1
fi
# Compatibility name for older instructions. Startup selection is automatic.
exec "$app_dir/offline-library" "$@"
