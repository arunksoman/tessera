# MariaDB publishes no official Alpine image, so build one from Alpine's package.
ARG ALPINE_VERSION=3.22
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache mariadb mariadb-client mariadb-server-utils
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && mkdir -p /run/mysqld /docker-entrypoint-initdb.d && chown -R mysql:mysql /run/mysqld /var/lib/mysql
VOLUME /var/lib/mysql
EXPOSE 3306
HEALTHCHECK --interval=2s --timeout=3s --retries=60 CMD mariadb-admin ping -h 127.0.0.1 --silent
ENTRYPOINT ["/entrypoint.sh"]
