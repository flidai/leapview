FROM docker.io/library/mysql:26.7@sha256:9d48c42f8341068f199116dfccb919b607c99765b5c61e549a548a43033471a4
# The fixture uses only mysqld and the mysql CLI. Remove the unrelated Python
# cloud tooling in mysql-shell and the unused root-to-mysql privilege helper.
RUN microdnf remove -y mysql-shell && rm -f /usr/local/bin/gosu && microdnf clean all
USER mysql
