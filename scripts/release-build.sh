#!/usr/bin/env bash
# Builds release archives and checksums into dist/. Same commit and version, same bytes.
# Usage: scripts/release-build.sh 0.1.0
set -euo pipefail

version=${1:?usage: release-build.sh VERSION}
if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "not a semantic version: $version" >&2
	exit 2
fi

epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
dist=dist
rm -rf "$dist"
mkdir -p "$dist"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os=${target%/*}
	arch=${target#*/}
	name=tess_${version}_${os}_${arch}
	bin=tess
	[[ $os == windows ]] && bin=tess.exe

	mkdir "$dist/$name"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
		-ldflags "-s -w -buildid= -X main.version=$version" -o "$dist/$name/$bin" ./cmd/tess
	cp LICENSE README.md "$dist/$name/"
	chmod 755 "$dist/$name/$bin"
	chmod 644 "$dist/$name/LICENSE" "$dist/$name/README.md"

	# Fixed times, owners, and order keep the archives reproducible.
	if [[ $os == windows ]]; then
		find "$dist/$name" -exec touch -d "@$epoch" {} +
		(cd "$dist" && find "$name" | LC_ALL=C sort | TZ=UTC zip -X -q "$name.zip" -@)
	else
		tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner -C "$dist" -cf - "$name" |
			gzip -n >"$dist/$name.tar.gz"
	fi
	rm -rf "${dist:?}/$name"
done

(cd "$dist" && sha256sum -- *.tar.gz *.zip >sha256sums.txt)
