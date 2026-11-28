# syntax=docker/dockerfile:1
# tess serve in a container. Base images are pinned by digest; Dependabot updates them.
# Build and target stages run on the build machine, so multi-arch needs no emulation.

FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS webapp
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS tess
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=webapp /src/web/build/app web/build/app
ARG TARGETOS TARGETARCH VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
	-ldflags "-s -w -buildid= -X main.version=$VERSION" -o /out/tess ./cmd/tess \
	&& mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=tess /out/tess /tess
COPY --from=tess --chown=65532:65532 /out/data /data
ENV TESSERA_CONFIG_DIR=/data
VOLUME /data
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/tess"]
CMD ["serve", "--addr", "0.0.0.0:8080", "--no-browser"]
