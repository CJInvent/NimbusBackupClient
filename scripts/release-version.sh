#!/bin/sh
# release-version.sh CHANNEL BUILD -- print the version a release of CHANNEL
# gets from this checkout (docs/V4-CLIENT-RELEASES.md section 1.2).
#
#   L.CC   from RELEASE_LINE
#   RR     one more than the highest STABLE RR tagged on L.CC, or 00
#   dev    L.CC.RR-dev.BUILD           (BUILD: the workflow run number)
#   alpha  L.CC.RR-alpha.N, beta L.CC.RR-beta.N  (N: earlier such tags + 1)
#   stable L.CC.RR
#
# Needs the tags (actions/checkout with fetch-depth: 0). Prints the version
# without the leading v. The same script, byte-identical, is in both
# repositories.
set -eu
channel=${1:?channel: dev, alpha, beta or stable}
build=${2:-0}
cd "$(dirname "$0")/.."

line=$(tr -d ' \r\n' < RELEASE_LINE)
printf '%s' "$line" | grep -Eq '^[0-9]+\.[0-9]{2,}$' || { echo "RELEASE_LINE is not L.CC: $line" >&2; exit 1; }
esc=$(printf '%s' "$line" | sed 's/\./\\./g')

last=$(git tag -l "v$line.*" | grep -E "^v$esc\.[0-9]{2,}$" | sed "s/^v$esc\.//" | sort -n | tail -n 1)
if [ -z "$last" ]; then rr=0; else rr=$(expr "$last" + 1); fi
base=$(printf '%s.%02d' "$line" "$rr")

case "$channel" in
  dev)
    printf '%s' "$build" | grep -Eq '^[1-9][0-9]*$' || { echo "dev needs a build number" >&2; exit 1; }
    echo "$base-dev.$build" ;;
  alpha|beta)
    n=$(git tag -l "v$base-$channel.*" | grep -Ec "^v$(printf '%s' "$base" | sed 's/\./\\./g')-$channel\.[0-9]+$" || true)
    echo "$base-$channel.$((n + 1))" ;;
  stable)
    echo "$base" ;;
  *) echo "unknown channel: $channel" >&2; exit 1 ;;
esac
