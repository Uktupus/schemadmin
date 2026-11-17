#!/usr/bin/env bash
#
# Runs the integration tests, including the PostgreSQL ones, against a
# throwaway PostgreSQL server that is created for the run and removed after.
#
#   scripts/pg-test.sh                     all integration tests
#   scripts/pg-test.sh -run Postgres -v    arguments are passed to "go test"
#   PG_BIN=/opt/homebrew/opt/postgresql@18/bin scripts/pg-test.sh
#                                          use a specific PostgreSQL install
#
# The server listens on a Unix socket only, inside a private temporary
# directory, so nothing is exposed on the network and no port can clash.
set -euo pipefail

bin=${PG_BIN:+$PG_BIN/}
for tool in initdb pg_ctl; do
  if ! command -v "${bin}${tool}" >/dev/null; then
    echo "pg-test: ${bin}${tool} not found; install PostgreSQL or set PG_BIN to its bin directory" >&2
    exit 1
  fi
done

root=$(cd "$(dirname "$0")/.." && pwd)

# Unix socket paths are limited to about 100 bytes, which rules out the long
# per-user temporary directories of macOS.
dir=$(mktemp -d /tmp/schemadmin-pg.XXXXXX)

cleanup() {
  "${bin}pg_ctl" -D "$dir/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT

"${bin}initdb" -D "$dir/data" -U postgres -A trust -E UTF8 --locale=C --no-sync >/dev/null
if ! "${bin}pg_ctl" -D "$dir/data" -l "$dir/server.log" -w \
  -o "-c listen_addresses='' -c unix_socket_directories='$dir' -c fsync=off" start >/dev/null; then
  cat "$dir/server.log" >&2
  exit 1
fi

export SCHEMADMIN_POSTGRES_DSN="postgres://postgres@/postgres?host=$dir&sslmode=disable"
echo "pg-test: $("${bin}postgres" --version) at $dir"

cd "$root/integration"
go test "$@" ./...
