#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) binary="$root/bin/model-list-plugin.exe" ;;
  *) binary="$root/bin/model-list-plugin" ;;
esac
exec "$binary" "$@"
