#!/usr/bin/env bash
#
# Tag every module of this repo at one version.
#
# A multi-module repo takes one tag per module: the root as vX.Y.Z, a submodule
# as <its directory>/vX.Y.Z. Miss one and that module has no release while its
# siblings do, which is invisible until someone runs go get on it.
#
#   scripts/tag.sh v0.2.0          create the tags locally
#   scripts/tag.sh v0.2.0 --push   and push them
#
# Modules are read from the tracked go.mod files, so a new one is tagged
# without touching this script.

set -euo pipefail

version=${1:-}
push=${2:-}

if ! printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'; then
	echo "usage: ${0##*/} vX.Y.Z [--push]" >&2
	exit 2
fi
if [ -n "$push" ] && [ "$push" != "--push" ]; then
	echo "usage: ${0##*/} vX.Y.Z [--push]" >&2
	exit 2
fi

cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain)" ]; then
	echo "refusing: the working tree is dirty, so the tag would not describe the commit" >&2
	exit 1
fi

# published reports whether the Go module proxy already serves this version.
# A published version is immutable: its hash is sealed in the checksum
# database, and re-tagging it makes every consumer fail on a checksum
# mismatch. Listing the known versions does not trigger a fetch of a new one.
published() {
	proxy_list=$(curl -fsS --max-time 10 "https://proxy.golang.org/$1/@v/list" 2>/dev/null) || {
		echo "  warning: the proxy is unreachable, cannot check whether $version is already published" >&2
		return 1
	}
	printf '%s\n' "$proxy_list" | grep -Fxq "$version"
}

modules=$(git ls-files | grep -E '(^|/)go\.mod$' | sed -e 's|/*go\.mod$||' -e 's|^$|.|' | sort -u)

# Check everything before writing anything: a half-tagged release is worse
# than none, and the second module is where the clash usually is.
for dir in $modules; do
	if [ "$dir" = "." ]; then tag=$version; else tag=$dir/$version; fi
	module=$(awk '/^module /{print $2; exit}' "$dir/go.mod")

	echo "$tag ($module)"

	if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
		echo "  refusing: the tag already exists locally" >&2
		exit 1
	fi
	if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
		echo "  refusing: the tag already exists on origin" >&2
		exit 1
	fi
	if published "$module"; then
		echo "  refusing: $module $version is already served by the Go proxy and sealed" >&2
		echo "  in the checksum database. Release the next version instead." >&2
		exit 1
	fi
done

for dir in $modules; do
	if [ "$dir" = "." ]; then tag=$version; name=$(basename "$PWD"); else tag=$dir/$version; name=$dir; fi
	git tag -a "$tag" -m "$name $version"
	echo "tagged $tag"
done

if [ "$push" = "--push" ]; then
	for dir in $modules; do
		if [ "$dir" = "." ]; then tag=$version; else tag=$dir/$version; fi
		git push origin "refs/tags/$tag"
	done
else
	echo
	echo "nothing pushed. To publish:"
	echo "  $0 $version --push"
fi
