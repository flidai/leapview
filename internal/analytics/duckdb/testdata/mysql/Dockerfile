FROM docker.io/library/mysql:26.10@sha256:f51c9c01c16b0eb5d81bb70ab7e7fcb3b96abc6a54114be3a94e8e569cae7815
# The fixture uses only mysqld and the mysql CLI. Remove the unrelated Python
# cloud tooling in mysql-shell and the unused root-to-mysql privilege helper.
RUN microdnf remove -y mysql-shell && rm -f /usr/local/bin/gosu && microdnf clean all
USER mysql
