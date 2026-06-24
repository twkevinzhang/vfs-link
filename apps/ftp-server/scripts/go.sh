#!/bin/sh
set -e

if [ -n "${GOROOT:-}" ] && [ ! -d "$GOROOT/src" ] && [ -d "$GOROOT/go/src" ]; then
  export GOROOT="$GOROOT/go"
fi

export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"

exec go "$@"
