#!/usr/bin/env bash
#
# Runs the integration tests, including the MySQL ones, against a throwaway
# mysqld that is created for the run and removed after.
#
#   scripts/mysql-test.sh                     all integration tests
#   scripts/mysql-test.sh -run Mysql -v       arguments are passed to "go test"
#   MYSQL_BIN=/opt/homebrew/opt/mysql/bin scripts/mysql-test.sh
#
# The server listens on a Unix socket only, inside a private temporary
# directory, so nothing is exposed on the network and no port can clash.
set -euo pipefail

bin=${MYSQL_BIN:+$MYSQL_BIN/}
for tool in mysqld mysql mysqladmin; do
  if ! command -v "${bin}${tool}" >/dev/null; then
    echo "mysql-test: ${bin}${tool} not found; install MySQL or set MYSQL_BIN to its bin directory" >&2
    exit 1
  fi
done

root=$(cd "$(dirname "$0")/.." && pwd)

dir=$(mktemp -d /tmp/schemadmin-mysql.XXXXXX)

cleanup() {
  if [ -f "$dir/mysqld.pid" ]; then
    "${bin}mysqladmin" --socket="$dir/mysql.sock" -u root shutdown >/dev/null 2>&1 || true
  fi
  rm -rf "$dir"
}
trap cleanup EXIT

"${bin}mysqld" --initialize-insecure --datadir="$dir/data" >/dev/null

"${bin}mysqld" \
  --datadir="$dir/data" \
  --socket="$dir/mysql.sock" \
  --pid-file="$dir/mysqld.pid" \
  --skip-networking \
  --character-set-server=utf8mb4 \
  --collation-server=utf8mb4_unicode_ci \
  --log-error="$dir/server.log" &

for _ in $(seq 1 120); do
  if "${bin}mysqladmin" --socket="$dir/mysql.sock" -u root ping >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
if ! "${bin}mysqladmin" --socket="$dir/mysql.sock" -u root ping >/dev/null 2>&1; then
  cat "$dir/server.log" >&2
  exit 1
fi

export SCHEMADMIN_MYSQL_DSN="root@unix($dir/mysql.sock)/mysql?multiStatements=true"
ver=$("${bin}mysqld" --verbose --help 2>/dev/null | head -n 1 || "${bin}mysql" --version)
echo "mysql-test: ${ver} socket at $dir"

cd "$root/integration"
go test "$@" ./...