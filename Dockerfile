FROM golang:1.26.2-trixie AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/borg ./cmd/borg

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/borg-genkey ./cmd/borg-genkey

FROM debian:trixie-slim

WORKDIR /app

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/borg /usr/local/bin/borg
COPY --from=builder /out/borg-genkey /usr/local/bin/borg-genkey
COPY config.example.yaml /app/config.yaml

EXPOSE 8000

CMD ["/usr/local/bin/borg", "--host", "0.0.0.0"]
