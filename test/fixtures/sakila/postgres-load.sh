#!/bin/sh
# Loads the Sakila sample database into its own database, "sakila", owned by the
# compose user. The dump assigns everything to a "postgres" role that this
# container does not have, so ownership and grants are rewritten as it loads.
set -eu

dir=/docker-entrypoint-initdb.d/sakila
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "CREATE DATABASE sakila OWNER \"$POSTGRES_USER\""
for f in "$dir/postgres-sakila-schema.sql" "$dir/postgres-sakila-insert-data-using-copy.sql"; do
	sed "s/ TO postgres;/ TO \"$POSTGRES_USER\";/" "$f" |
		psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d sakila
done
