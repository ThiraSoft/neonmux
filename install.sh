#!/bin/sh
# Installs neonmux.
#   macOS: bundle in ~/Applications (Spotlight, Launchpad, Raycast…)
#   Linux: .desktop entry (rofi, GNOME, KDE…) and icon
# Either way, the `neonmux` command lands in ~/.local/bin.
#   ./install.sh               installs or updates
#   ./install.sh --uninstall   removes everything
set -eu
cd "$(dirname "$0")"

# stop_daemon stops the daemon if it answers (nothing if it is not there).
# When run from a neonmux pane, this script loses its terminal along with
# the daemon, so it ignores SIGHUP and writes nothing afterwards.
stop_daemon() {
	[ -x "$1" ] || return 0
	trap '' HUP
	NEONMUX_SOCK= "$1" kill-server >/dev/null 2>&1 || true
}

if [ "$(uname)" = "Linux" ]; then
	BIN_DIR="$HOME/.local/bin"
	DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
	DESKTOP="$DATA/applications/neonmux.desktop"
	ICON="$DATA/icons/hicolor/256x256/apps/neonmux.png"
	if [ "${1:-}" = "--uninstall" ]; then
		stop_daemon "$BIN_DIR/neonmux"
		rm -f "$BIN_DIR/neonmux" "$DESKTOP" "$ICON"
		echo "neonmux uninstalled (the config and saved sessions stay in ~/.config and ~/.local/state)." 2>/dev/null || true
		exit 0
	fi
	echo "› build"
	CGO_ENABLED=0 go build -o neonmux .
	mkdir -p "$BIN_DIR" "$(dirname "$DESKTOP")" "$(dirname "$ICON")"
	install -m 755 neonmux "$BIN_DIR/neonmux"
	ICONSET="$(mktemp -d)/neonmux.iconset"
	./neonmux icon "$ICONSET"
	cp "$ICONSET/icon_256x256.png" "$ICON"
	rm -rf "$(dirname "$ICONSET")"
	# Exec= follows the Desktop Entry spec: the path is quoted (a space in
	# $HOME would break the launcher), \ " ` $ are escaped twice (the value
	# is first read as a string) and % is doubled.
	EXEC=$(printf '%s' "$BIN_DIR/neonmux" | sed -e 's/\\/\\\\\\\\/g' -e 's/["`$]/\\\\&/g' -e 's/%/%%/g')
	cat > "$DESKTOP" <<DESK
[Desktop Entry]
Type=Application
Name=neonmux
Comment=Terminals, files, images and sound in a single window
Exec="$EXEC"
Icon=neonmux
Terminal=false
Categories=System;TerminalEmulator;
StartupWMClass=neonmux
DESK
	command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$DATA/applications" || true
	echo "neonmux installed: app launcher (\"neonmux\") or \`neonmux\` in a terminal."
	exit 0
fi

APP="$HOME/Applications/neonmux.app"
BIN_DIR="$HOME/.local/bin"
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister

if [ "${1:-}" = "--uninstall" ]; then
	stop_daemon "$APP/Contents/MacOS/neonmux"
	[ -d "$APP" ] && "$LSREGISTER" -u "$APP" 2>/dev/null || true
	rm -rf "$APP"
	if [ -L "$BIN_DIR/neonmux" ]; then rm -f "$BIN_DIR/neonmux"; fi
	echo "neonmux uninstalled (the config and saved sessions stay in ~/.config and ~/.local/state)." 2>/dev/null || true
	exit 0
fi

echo "› build"
go build -o neonmux .

echo "› bundle $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp neonmux "$APP/Contents/MacOS/neonmux"

ICONSET="$(mktemp -d)/neonmux.iconset"
./neonmux icon "$ICONSET"
iconutil -c icns -o "$APP/Contents/Resources/neonmux.icns" "$ICONSET"
rm -rf "$(dirname "$ICONSET")"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>neonmux</string>
	<key>CFBundleDisplayName</key><string>neonmux</string>
	<key>CFBundleIdentifier</key><string>local.neonmux</string>
	<key>CFBundleExecutable</key><string>neonmux</string>
	<key>CFBundleIconFile</key><string>neonmux</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>0.2</string>
	<key>CFBundleVersion</key><string>$(date +%Y%m%d%H%M)</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

# Ad hoc signature (enough for local use), then registration with
# LaunchServices and Spotlight so the app is found right away.
codesign --force --sign - "$APP" >/dev/null 2>&1 || true
"$LSREGISTER" -f "$APP" >/dev/null 2>&1 || true
mdimport "$APP" >/dev/null 2>&1 || true

echo "› link $BIN_DIR/neonmux"
mkdir -p "$BIN_DIR"
ln -sf "$APP/Contents/MacOS/neonmux" "$BIN_DIR/neonmux"

echo "neonmux installed: Spotlight (cmd+space, \"neonmux\") or \`neonmux\` in a terminal."
