# Patroni is built locally so its PostgreSQL base and Python dependencies are
# explicit and reviewable. The runner always rebuilds this disposable image.
FROM docker.io/library/postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873

COPY requirements.lock /tmp/requirements.lock
RUN apk add --no-cache 'python3=3.14.7-r1' \
    && python3 -m venv /opt/patroni \
    && /opt/patroni/bin/pip install --no-cache-dir --disable-pip-version-check \
        --requirement /tmp/requirements.lock

COPY patroni-entrypoint.sh /usr/local/bin/patroni-entrypoint.sh
RUN chmod 0755 /usr/local/bin/patroni-entrypoint.sh

USER postgres
ENTRYPOINT ["/usr/local/bin/patroni-entrypoint.sh"]
