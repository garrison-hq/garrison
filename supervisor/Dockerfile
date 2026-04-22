FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o /out/supervisor ./cmd/supervisor

# alpine (not distroless) because M1's ORG_OS_FAKE_AGENT_CMD is shell-syntax
# (sh -c "…") and the subprocess lifecycle manager exec's that literally.
# M2 swaps the fake agent for the real `claude` CLI; we can re-evaluate the
# runtime base then.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/supervisor /supervisor
ENTRYPOINT ["/supervisor"]
