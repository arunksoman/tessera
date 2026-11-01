#!/usr/bin/env bash
# Fails if a direct dependency has no justification in section 11 of the design.
set -euo pipefail

section=$(sed -n '/^## 11\. Dependencies/,/^## 12\./p' design-spec/design.md)
status=0
while read -r dep; do
	if ! grep -qF "\`$dep\`" <<<"$section"; then
		echo "no justification in design-spec/design.md section 11: $dep" >&2
		status=1
	fi
done < <(go list -m -f '{{if not (or .Indirect .Main)}}{{.Path}}{{end}}' all)
exit $status
