# syntax=docker/dockerfile:1.5
FROM golang:1.26.2-trixie AS builder

WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 GO111MODULE=off go build -trimpath -ldflags="-s -w" -o /out/dummy-openai .

FROM scratch

COPY --from=builder /out/dummy-openai /dummy-openai

EXPOSE 8000
CMD ["/dummy-openai"]
