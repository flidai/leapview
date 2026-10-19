FROM docker.io/library/mysql:26.7@sha256:893418c8a7a34c60d2d961cc30ef136095da141478562ed2101ae3f5faba9c3f
# The fixture uses only mysqld and the mysql CLI. Remove the unrelated Python
# cloud tooling in mysql-shell and the unused root-to-mysql privilege helper.
RUN microdnf remove -y mysql-shell && rm -f /usr/local/bin/gosu && microdnf clean all
USER mysql
