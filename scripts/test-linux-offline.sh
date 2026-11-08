#!/bin/sh
# Run the packaged-app smoke test with no network interface except loopback.
set -eu
if [ "$#" -ne 3 ]; then
  printf 'Usage: %s APP_EXECUTABLE FIXTURE_ZIM COLLISION_FIXTURE_ZIM\n' "$0" >&2
  exit 2
fi
app=$(realpath -e -- "$1")
fixture=$(realpath -e -- "$2")
collision=$(realpath -e -- "$3")
if [ ! -x "$app" ] || [ ! -f "$fixture" ] || [ ! -f "$collision" ]; then
  printf '%s\n' 'Expected an executable app and two existing ZIM fixtures.' >&2
  exit 2
fi
uid=$(id -u)
gid=$(id -g)
if [ "$uid" -eq 0 ]; then
  printf '%s\n' 'Run as a regular user, not root.' >&2
  exit 2
fi
root=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
# The current user maps to its own UID; subordinate IDs provide namespace root
# solely to enable loopback. Drop back to the original UID before launching.
exec unshare --user --map-current-user --map-auto --net --setuid 0 --setgid 0 sh -c '
  set -eu
  uid=$1 gid=$2 app=$3 fixture=$4 collision=$5 smoke=$6
  ip link set lo up
  if [ "$(ip -o link show | wc -l)" -ne 1 ] || [ -n "$(ip route show)" ] || [ -n "$(ip -6 route show | grep -v "^::1 " || true)" ]; then
    printf "%s\n" "Unexpected network interfaces or routes; refusing offline test." >&2
    exit 1
  fi
  printf "%s\n" "Network namespace: loopback only, no external routes."
  if [ "${OFFLINE_LIBRARY_LIMIT_SCOPE:-0}" = 1 ]; then
    exec setpriv --reuid "$uid" --regid "$gid" --clear-groups \
      systemd-run --user --scope -p CPUQuota=100% -p MemoryHigh=1G -p MemoryMax=2G -p MemorySwapMax=0 \
      nice -n 15 env OFFLINE_LIBRARY_SMOKE_APP="$app" OFFLINE_LIBRARY_SMOKE_ZIM="$fixture" \
      OFFLINE_LIBRARY_SMOKE_COLLISION_ZIM="$collision" xvfb-run -a node "$smoke"
  fi
  exec setpriv --reuid "$uid" --regid "$gid" --clear-groups \
    env OFFLINE_LIBRARY_SMOKE_APP="$app" OFFLINE_LIBRARY_SMOKE_ZIM="$fixture" \
    OFFLINE_LIBRARY_SMOKE_COLLISION_ZIM="$collision" xvfb-run -a node "$smoke"
' sh "$uid" "$gid" "$app" "$fixture" "$collision" "$root/desktop/shell/test/smoke.cjs"
