#!/usr/bin/env bash
# Print a TCP port nothing is listening on.
#
# Why this exists rather than a constant in the Makefile: a fixed port collided
# with an ssh tunnel that was already listening on it, so the test client
# connected to an unrelated database, failed to authenticate, and reported what
# looked like a bug in the code under test. Choosing at run time removes a whole
# class of confusing failure.
set -euo pipefail
for port in $(seq 55600 55700); do
  if ! lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "$port"; exit 0
  fi
done
echo "no free port in 55600-55700" >&2
exit 1
