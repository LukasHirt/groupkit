#!/bin/sh
# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

# Starts the example server on the host's loopback address, runs check.py
# against a running emulator, and stops the server. The emulator reaches the
# host's loopback address as 10.0.2.2.
#
# Environment: SERVER (path of the built example server), DAVX5_APK (path of
# the APK) and, optionally, OUT_DIR (where a log and a screenshot go when the
# check fails). The script exits with the check's status.
set -u

: "${SERVER:?set SERVER to the built example server}"
: "${DAVX5_APK:?set DAVX5_APK to the DAVx5 APK}"
OUT_DIR=${OUT_DIR:-$(mktemp -d)}
mkdir -p "$OUT_DIR"

GROUPKIT_PASSWORD=${GROUPKIT_PASSWORD:-ci-check-password-1}
PORT=${PORT:-8080}
export GROUPKIT_PASSWORD DAVX5_APK
export GROUPKIT_URL="http://127.0.0.1:$PORT/dav/"

"$SERVER" -addr "127.0.0.1:$PORT" -data "$(mktemp -d)" >"$OUT_DIR/server.log" 2>&1 &
server=$!
trap 'kill $server' EXIT

for _ in $(seq 1 50); do
  curl -s -o /dev/null "http://127.0.0.1:$PORT/" && break
  sleep 0.2
done

python3 -I "$(dirname "$0")/check.py"
status=$?
if [ "$status" -ne 0 ]; then
  adb logcat -d >"$OUT_DIR/logcat.txt" 2>&1
  adb exec-out screencap -p >"$OUT_DIR/screen.png" 2>/dev/null
fi
exit $status
