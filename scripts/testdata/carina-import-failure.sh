#!/bin/sh
set -eu

if [ "$#" -ne 4 ] || [ "$1" != "auth" ] || [ "$2" != "import" ] || [ "$3" != "cc-switch" ]; then
  echo "unexpected test helper invocation" >&2
  exit 2
fi

attempts_file="${CCSWITCH_ATTEMPTS_FILE:?}"
attempts=0
if [ -f "$attempts_file" ]; then
  attempts="$(sed -n '1p' "$attempts_file")"
fi
attempts=$((attempts + 1))
printf '%s\n' "$attempts" > "$attempts_file"

sleep 0.4
echo "carina: provider setup: Relay profile: status 503; endpoint rejects this client type" >&2
exit 1
