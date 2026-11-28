#!/usr/bin/env bash
# Takes the TUI screenshots used in the docs, in the light and dark themes, with VHS in Docker
# against the Sakila database in the compose PostgreSQL. Nothing is written to the database.
#
# Usage, from the repository root after `docker compose up -d --wait`:
#   docs/scripts/tui-screenshots.sh [VERSION]
set -euo pipefail

version=${1:-dev}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$version" -o "$work/tess" ./cmd/tess
out=docs/src/assets/screens

for scheme in light dark; do
	if [ "$scheme" = light ]; then
		colors='"background":"#F7F5F0","foreground":"#1C1F24","cursor":"#2F5DA8","selection":"#DFE7F5"'
	else
		colors='"background":"#14161A","foreground":"#E7E9EC","cursor":"#7AA5EB","selection":"#23324A"'
	fi
	# ANSI colours are unused by the truecolor TUI, but VHS requires them.
	ansi='"black":"#1C1F24","red":"#B3261E","green":"#1B7339","yellow":"#8A5A00","blue":"#2F5DA8","magenta":"#7B3FA0","cyan":"#0F766E","white":"#E7E9EC","brightBlack":"#5B616B","brightRed":"#F2726A","brightGreen":"#5CC98A","brightYellow":"#E5B84B","brightBlue":"#7AA5EB","brightMagenta":"#BE8FE0","brightCyan":"#4FC4B5","brightWhite":"#FFFFFF"'
	cat >"$work/tui.tape" <<EOF
Output "out.gif"
Set Shell bash
Set FontSize 15
Set Width 1360
Set Height 820
Set Padding 12
Set Theme {$colors,$ansi}
Set TypingSpeed 0

Hide
Type "export TESSERA_CONFIG_DIR=/tmp/tess TESSERA_PASSPHRASE=docs PATH=/work:\$PATH COLORTERM=truecolor" Enter
Type "printf 'tessera_dev\n' | tess profile add sakila --tile postgres --host host.docker.internal --port 55418 --user tessera --database sakila --tls disable --password-stdin" Enter
Type \`sed -i '1i theme = "$scheme"' /tmp/tess/config.toml && clear\` Enter
Sleep 1s
Type "tess tui sakila" Enter
Sleep 3s
Show

Type "SELECT c.name AS category, count(*) AS films, round(avg(f.rental_rate), 2) AS avg_rate FROM film f JOIN film_category fc USING (film_id) JOIN category c USING (category_id) GROUP BY c.name ORDER BY films DESC;"
# VHS has no function keys, so run through the command palette.
Ctrl+K
Type "run statement"
Enter
Sleep 2s
Screenshot "/work/tui-sql-$scheme.png"
Ctrl+K
Type "tab"
Sleep 1s
Screenshot "/work/tui-palette-$scheme.png"
Escape
Sleep 500ms
Ctrl+K
Type "help"
Enter
Sleep 1s
Screenshot "/work/tui-help-$scheme.png"
EOF
	MSYS_NO_PATHCONV=1 docker run --rm --add-host host.docker.internal:host-gateway \
		-v "$(cd "$work" && pwd -W 2>/dev/null || pwd)":/work -w /work ghcr.io/charmbracelet/vhs /work/tui.tape
	cp "$work"/tui-*-"$scheme".png "$out"/
done
ls "$out"/tui-*
