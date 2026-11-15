FROM golang:1.22-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN apk add --no-cache ca-certificates && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/registry-janitor ./cmd/registry-janitor

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/registry-janitor /registry-janitor
USER 65532:65532
ENTRYPOINT ["/registry-janitor"]
