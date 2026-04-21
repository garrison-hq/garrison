FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${VERSION}" -o /out/supervisor ./cmd/supervisor
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/supervisor /supervisor
ENTRYPOINT ["/supervisor"]
