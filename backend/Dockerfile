# The backend image.
#
#   docker build -t lgtm/backend:v1 backend/
#   docker save lgtm/backend:v1 | sudo k3s ctr images import -
#
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 gives one static binary, so the final stage needs no Go and no
# matching libc. -trimpath keeps the build paths of this machine out of it.
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /lgtm-backend .

FROM rust:1.98.0-slim-bookworm AS toolchain

RUN rustup target add wasm32-wasip1 \
 && rm -rf \
      /usr/local/rustup/toolchains/*/lib/rustlib/x86_64-unknown-linux-gnu/lib \
      /usr/local/rustup/toolchains/*/bin/cargo \
      /usr/local/rustup/toolchains/*/bin/rustdoc \
      /usr/local/rustup/toolchains/*/share \
      /usr/local/cargo/registry

FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install --yes --no-install-recommends libstdc++6 \
 && rm -rf /var/lib/apt/lists/*

ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    PATH=/usr/local/cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY --from=toolchain /usr/local/rustup /usr/local/rustup
COPY --from=toolchain /usr/local/cargo /usr/local/cargo

RUN useradd --uid 10001 --create-home --shell /usr/sbin/nologin lgtm
COPY --from=build /lgtm-backend /usr/local/bin/lgtm-backend
USER 10001
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/lgtm-backend"]
