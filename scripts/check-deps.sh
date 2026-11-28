#!/usr/bin/env bash
# Fails if a direct dependency of tess or of a plugin in plugins/ has no justification in
# section 11 of the design.
set -euo pipefail

section=$(sed -n '/^## 11\. Dependencies/,/^## 12\./p' design-spec/design.md)
status=0
for mod in . plugins/*/; do
	while read -r dep; do
		if ! grep -qF "\`$dep\`" <<<"$section"; then
			echo "no justification in design-spec/design.md section 11: $dep (in $mod)" >&2
			status=1
		fi
	done < <(cd "$mod" && go list -m -f '{{if not (or .Indirect .Main)}}{{.Path}}{{end}}' all)
done
exit $status
