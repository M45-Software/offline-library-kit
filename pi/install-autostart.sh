#!/bin/sh
# Installs only user-level launchers; pass the local project directory.
set -eu
project=$(CDPATH= cd -- "${1:-.}" && pwd)
case "$project" in *' '*) echo 'Use a project path without spaces for these desktop launchers.' >&2; exit 1;; esac
case "$project" in *[!a-zA-Z0-9_./-]*) echo 'Use a path containing only letters, numbers, slash, dot, dash and underscore.' >&2; exit 1;; esac
mkdir -p "$HOME/.config/autostart"
for f in "$HOME/.config/autostart/offline-library-server.desktop" "$HOME/.config/autostart/offline-library-browser.desktop" "$HOME/.config/autostart/offline-library-background.desktop"; do
  if [ -e "$f" ]; then echo "Refusing to overwrite $f" >&2; exit 1; fi
done
cat > "$HOME/.config/autostart/offline-library-server.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Offline Library Server
Exec=$project/pi/start-library.sh
Terminal=false
DESKTOP
cat > "$HOME/.config/autostart/offline-library-browser.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Open Offline Library
Exec=$project/pi/open-library.sh
Terminal=false
DESKTOP
cat > "$HOME/.config/autostart/offline-library-background.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Offline Library Background
Exec=$project/pi/set-black-background.sh
Terminal=false
DESKTOP
printf '%s\n' 'Launchers installed. Log in again to test them.'
