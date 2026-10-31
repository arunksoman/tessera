#!/bin/sh
# First boot: create the system tables, the app user, then run init scripts.
set -eu

if [ ! -d /var/lib/mysql/mysql ]; then
	chown -R mysql:mysql /var/lib/mysql
	mariadb-install-db --user=mysql --datadir=/var/lib/mysql --skip-test-db --auth-root-authentication-method=normal >/dev/null

	mariadbd --user=mysql --skip-networking --socket=/run/mysqld/boot.sock &
	pid=$!
	until mariadb-admin --socket=/run/mysqld/boot.sock ping --silent 2>/dev/null; do sleep 0.2; done

	mariadb --socket=/run/mysqld/boot.sock <<SQL
ALTER USER 'root'@'localhost' IDENTIFIED BY '${MARIADB_ROOT_PASSWORD}';
CREATE USER 'root'@'%' IDENTIFIED BY '${MARIADB_ROOT_PASSWORD}';
GRANT ALL ON *.* TO 'root'@'%' WITH GRANT OPTION;
CREATE DATABASE IF NOT EXISTS \`${MARIADB_DATABASE}\`;
CREATE USER '${MARIADB_USER}'@'%' IDENTIFIED BY '${MARIADB_PASSWORD}';
GRANT ALL ON \`${MARIADB_DATABASE}\`.* TO '${MARIADB_USER}'@'%';
FLUSH PRIVILEGES;
SQL

	for f in /docker-entrypoint-initdb.d/*.sql; do
		[ -e "$f" ] && mariadb --socket=/run/mysqld/boot.sock -uroot -p"${MARIADB_ROOT_PASSWORD}" "${MARIADB_DATABASE}" <"$f"
	done

	mariadb-admin --socket=/run/mysqld/boot.sock -uroot -p"${MARIADB_ROOT_PASSWORD}" shutdown
	wait "$pid"
fi

# Alpine's packaged config sets skip-networking, so turn networking back on.
exec mariadbd --user=mysql --skip-networking=0 --bind-address=0.0.0.0 --skip-name-resolve --console
