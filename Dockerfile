# One Dockerfile builds every binary; the service picks which via CMD_NAME.
# Avoids four near-identical files drifting apart.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Copy the module files alone first. This layer is cached and only
# re-downloads dependencies when go.mod or go.sum actually change, instead of
# on every source edit.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG CMD_NAME=api
# CGO_ENABLED=0 produces a static binary, so the runtime stage needs no libc.
RUN CGO_ENABLED=0 go build -trimpath -o /out/app ./cmd/${CMD_NAME}

FROM alpine:3.21
# busybox wget ships with alpine and is what the Compose healthcheck calls.
RUN adduser -D -u 10001 app
COPY --from=build /out/app /usr/local/bin/app
USER app
ENTRYPOINT ["/usr/local/bin/app"]
