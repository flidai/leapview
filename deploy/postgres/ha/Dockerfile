# Patroni is built locally so its PostgreSQL base and Python dependencies are
# explicit and reviewable. The runner always rebuilds this disposable image.
FROM docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8

COPY requirements.lock /tmp/requirements.lock
RUN apk add --no-cache 'python3=3.14.7-r1' \
    && python3 -m venv /opt/patroni \
    && /opt/patroni/bin/pip install --no-cache-dir --disable-pip-version-check \
        --requirement /tmp/requirements.lock

COPY patroni-entrypoint.sh /usr/local/bin/patroni-entrypoint.sh
RUN chmod 0755 /usr/local/bin/patroni-entrypoint.sh

USER postgres
ENTRYPOINT ["/usr/local/bin/patroni-entrypoint.sh"]
