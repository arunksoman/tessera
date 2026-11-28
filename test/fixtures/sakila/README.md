# Sakila sample database

The compose databases load Sakila into a database named `sakila`, next to the `tessera_test` fixture that the tests use. The SQLite tile's tests load the SQLite port into a temporary file. The files here are unmodified copies of third-party scripts.

| Files | Source | License |
|---|---|---|
| `mysql/sakila-schema.sql`, `mysql/sakila-data.sql` | Oracle's [sakila-db.tar.gz](https://downloads.mysql.com/docs/sakila-db.tar.gz), schema version 1.5 | BSD 3-Clause, in each file's header |
| `postgres/postgres-sakila-schema.sql`, `postgres/postgres-sakila-insert-data-using-copy.sql` | [jOOQ/sakila](https://github.com/jOOQ/sakila) at commit `e089a5b1ec9af0df7a9c6a5d47d49fa1736a4e84` | BSD 2-Clause, in [postgres/LICENSE](postgres/LICENSE) |
| `sqlite/sqlite-sakila-schema.sql`, `sqlite/sqlite-sakila-insert-data.sql` | the same jOOQ commit, `sqlite-sakila-db/` | BSD, in each file's header (DB Software Laboratory) |

SHA-256 of the copies:

```
b32170e1e2ad5828749b61a5ec896155bcd143104b076e5ee8a3a3b013f44915  mysql/sakila-schema.sql
8c228c678cec6ea9e5145ea868f48be87982252e806a547dcddb67758cadf174  mysql/sakila-data.sql
eaa8cfc2fd358e70617d31ed3638196982944749929f02e90b980457855bfa48  postgres/postgres-sakila-schema.sql
072e793e9f4f2686b46de8904e1e5ca90c02cbc1873d69ed15401aef610ab52b  postgres/postgres-sakila-insert-data-using-copy.sql
b1db76b3a5192f98493901a9ac40fecb9dba918feb5604b7ef6b74230a385f12  sqlite/sqlite-sakila-schema.sql
4d69a56acbd3f60501e45737353707df0cf51fdb7fcab65cc9db3a72c8c85f34  sqlite/sqlite-sakila-insert-data.sql
```

Oracle's scripts serve MySQL and MariaDB, and `mysql-grant.sql` gives the compose user access. The jOOQ port serves PostgreSQL, because the MySQL dump does not load there. Its dump assigns ownership to a `postgres` role, which the containers lack, so `postgres-load.sh` rewrites that to the compose user while loading.
