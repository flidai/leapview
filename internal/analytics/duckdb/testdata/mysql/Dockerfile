FROM docker.io/library/mysql:26.10@sha256:3819b2b23835c4a73c8b022fafc33c8095c0f81c929a8b7a84613d2fba6c52f5
# The fixture uses only mysqld and the mysql CLI. Remove the unrelated Python
# cloud tooling in mysql-shell and the unused root-to-mysql privilege helper.
RUN microdnf remove -y mysql-shell && rm -f /usr/local/bin/gosu && microdnf clean all
USER mysql
