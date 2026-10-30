# GoPress container image: a static binary on an empty base image.
#   make image                   (sets VERSION and COMMIT)
#   docker run -v gopress:/data -p 8080:8080 IMAGE
# See docs/deploy.md.

# The base image is pinned by digest so that the CA bundle and the
# toolchain only change on purpose.
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ARG VERSION=dev
ARG COMMIT=unknown
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -buildvcs=false \
      -ldflags "-s -w -buildid= -X github.com/theinvisible/gopress/internal/buildinfo.Version=${VERSION} -X github.com/theinvisible/gopress/internal/buildinfo.Commit=${COMMIT}" \
      -o /out/gopress ./cmd/gopress
# A user without a shell or home for the final image, and the data
# directory owned by it (a new named volume copies this ownership).
RUN mkdir -p /out/etc /out/root/data && \
    echo 'gopress:x:65532:65532:gopress:/data:/sbin/nologin' > /out/etc/passwd && \
    echo 'gopress:x:65532:' > /out/etc/group && \
    chmod 0750 /out/root/data

FROM scratch
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="GoPress" \
      org.opencontainers.image.description="Single-binary WordPress alternative in Go" \
      org.opencontainers.image.source="https://github.com/theinvisible/gopress" \
      org.opencontainers.image.licenses="GPL-2.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/etc/passwd /out/etc/group /etc/
COPY --from=build --chown=65532:65532 /out/root/ /
COPY --from=build /out/gopress /gopress
USER 65532:65532
ENV GOPRESS_ADDR=:8080 GOPRESS_DATA=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/gopress", "healthcheck"]
ENTRYPOINT ["/gopress"]
CMD ["serve"]
