FROM docker.io/library/mysql:26.10@sha256:7b16ec7caa94ca7e92f4c5ec287de69d2e8738db056ceb629022951a6f6d910b
# The fixture uses only mysqld and the mysql CLI. Remove the unrelated Python
# cloud tooling in mysql-shell and the unused root-to-mysql privilege helper.
RUN microdnf remove -y mysql-shell && rm -f /usr/local/bin/gosu && microdnf clean all
USER mysql
