# Same Go toolchain and source commit as RELEASE.2025-09-07T16-13-09Z.
# Build our disposable test fixture without pulling a prebuilt MinIO image.
FROM golang:1.24.6-bookworm@sha256:ab1d1823abb55a9504d2e3e003b75b36dbeb1cbcc4c92593d85a84ee46becc6c AS build
WORKDIR /src
RUN curl --fail --location --retry 3 https://codeload.github.com/minio/minio/tar.gz/07c3a429bfed433e49018cb0f78a52145d4bedeb -o /tmp/minio.tar.gz \
    && echo '8819e3e7817e46b7b3798f8f200ead208562e571563c2e040352378031abe9f2  /tmp/minio.tar.gz' | sha256sum --check \
    && tar -xzf /tmp/minio.tar.gz --strip-components=1 \
    && rm /tmp/minio.tar.gz
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOMAXPROCS=4
RUN go build -p 4 -mod=readonly -trimpath -ldflags '-s -w -X github.com/minio/minio/cmd.Version=2025-09-07T16:13:09Z -X github.com/minio/minio/cmd.CopyrightYear=2025 -X github.com/minio/minio/cmd.ShortCommitID=07c3a429bfed -X github.com/minio/minio/cmd.ReleaseTag=RELEASE.2025-09-07T16-13-09Z -X github.com/minio/minio/cmd.CommitID=07c3a429bfed433e49018cb0f78a52145d4bedeb' -o /minio . \
    && go clean -cache -modcache

RUN mkdir -p /runtime/data /runtime/root/.minio/certs /runtime/tmp && chmod 1777 /runtime/tmp

FROM scratch
ENV HOME=/root
COPY --from=build --chown=65532:65532 /runtime/ /
COPY --from=build /minio /minio
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /src/LICENSE /LICENSE
USER 65532:65532
EXPOSE 9000
ENTRYPOINT ["/minio"]
CMD ["server", "/data"]
