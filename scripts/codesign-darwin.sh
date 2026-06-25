#!/bin/sh
#
# Sign local Darwin builds with a stable identity when one is available.
# Falling back to ad-hoc signing keeps builds working on machines without
# a codesigning certificate, but ad-hoc signatures change identity on rebuild
# and can churn macOS TCC grants such as Accessibility.

set -eu

if [ "$(uname -s)" != "Darwin" ]; then
    exit 0
fi

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
    echo "usage: $0 <binary> [identifier]" >&2
    exit 2
fi

binary="$1"
identifier="${2:-com.donworks.mcplexer}"

if [ ! -f "$binary" ]; then
    echo "codesign: binary not found: $binary" >&2
    exit 1
fi

identity="${MCPLEXER_CODESIGN_IDENTITY:-${MCPLEXER_SIGN_ID:-}}"
if [ -z "$identity" ]; then
    identity=$(
        security find-identity -v -p codesigning 2>/dev/null |
            awk -F '"' '/"[^"]+"/ { print $2; exit }'
    )
fi

if [ -z "$identity" ]; then
    identity="-"
fi

if codesign --force --sign "$identity" --identifier "$identifier" "$binary" >/dev/null 2>&1; then
    if [ "$identity" = "-" ]; then
        echo "codesign: ad-hoc signed $binary ($identifier)" >&2
    else
        echo "codesign: signed $binary with $identity ($identifier)" >&2
    fi
    exit 0
fi

if [ "$identity" != "-" ]; then
    echo "codesign: identity '$identity' failed for $binary; falling back to ad-hoc" >&2
    codesign --force --sign - --identifier "$identifier" "$binary"
    exit 0
fi

echo "codesign: failed to sign $binary" >&2
exit 1
