#!/bin/sh
# Run the experimental reader rebuild with a loopback-only network and limits.
set -eu
if [ "$#" -ne 2 ]; then
  printf 'Usage: %s PREPARED_WORKSPACE PYTHON_VENV_EXECUTABLE\n' "$0" >&2
  exit 2
fi
work=$(CDPATH= cd -P -- "$1" && pwd)
python_dir=$(CDPATH= cd -P -- "$(dirname -- "$2")" && pwd)
python="$python_dir/$(basename -- "$2")"
if [ ! -x "$python" ] || [ ! -f "$work/PREPARED.txt" ] || [ ! -f "$work/offline-builder.py" ] || [ ! -f "$work/link_warning_policy.py" ] || [ ! -x "$work/optional-qmake" ]; then
  printf '%s\n' 'Expected a prepared workspace and executable Python environment.' >&2
  exit 2
fi
# Compiler flags and PATH are passed through other build tools as strings.
for path_to_check in "$work" "$python_dir"; do
  case "$path_to_check" in *[!A-Za-z0-9_./-]*) printf '%s\n' 'Workspace and Python paths must use only ASCII letters, digits, slash, dot, underscore or hyphen.' >&2; exit 2;; esac
done
if [ -e "$work/BUILD_native_static" ] || [ -e "$work/build-run.log" ] || [ -e "$work/REBUILD-INCOMPLETE" ]; then
  printf '%s\n' 'Workspace already contains a build or prior run; prepare a new workspace.' >&2
  exit 2
fi
app_uid=$(id -u)
app_gid=$(id -g)
if [ "$app_uid" -eq 0 ]; then
  printf '%s\n' 'Run as a regular user, not root.' >&2
  exit 2
fi
printf 'Building offline; log: %s/build-run.log\n' "$work"
# Namespace root is used only to configure loopback, then dropped before build.
exec unshare --user --map-current-user --map-auto --net --setuid 0 --setgid 0 sh -c '
  set -eu
  app_uid=$1 app_gid=$2 work=$3 python=$4 python_dir=$5
  ip link set lo up
  if [ "$(ip -o link show | wc -l)" -ne 1 ] || [ -n "$(ip route show)" ] || [ -n "$(ip -6 route show | grep -v "^::1 " || true)" ]; then
    printf "%s\n" "Unexpected network interfaces or routes; refusing build." >&2
    exit 1
  fi
  exec setpriv --reuid "$app_uid" --regid "$app_gid" --clear-groups \
    systemd-run --user --scope -p CPUQuota=100% -p MemoryHigh=1G -p MemoryMax=2G -p MemorySwapMax=0 \
    nice -n 15 ionice -c3 sh -c '\''
      set -eu
      work=$1 python=$2 python_dir=$3
      set -C
      : > "$work/REBUILD-INCOMPLETE"
      env -i PATH="$python_dir:/usr/local/bin:/usr/bin:/bin" LANG=C.UTF-8 PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 \
        TMPDIR="$work/tmp" OLK_REBUILD_DIR="$work" KBUILD_NINJA_COMMAND="ninja -j1" KBUILD_QMAKE_COMMAND="$work/optional-qmake" \
        MAKEFLAGS=-j1 MESON_TESTTHREADS=1 SKIP_BIG_MEMORY_TEST=1 CMAKE_BUILD_PARALLEL_LEVEL=1 CARGO_BUILD_JOBS=1 \
        CFLAGS="-ffile-prefix-map=$work=/build/olk" CXXFLAGS="-ffile-prefix-map=$work=/build/olk" \
        "$python" "$work/offline-builder.py" > "$work/build-run.log" 2>&1
      rm -- "$work/REBUILD-INCOMPLETE"
      printf "%s\n" "Offline reader build completed."
    '\'' sh "$work" "$python" "$python_dir"
' sh "$app_uid" "$app_gid" "$work" "$python" "$python_dir"
