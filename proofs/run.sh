#!/bin/sh
set -eu
cd -- "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [ "$#" -eq 0 ]; then set -- lake build; fi
exec elan run "$(cat lean-toolchain)" "$@"
