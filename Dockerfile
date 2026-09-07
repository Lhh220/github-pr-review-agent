FROM golang:1.25.0-alpine3.22 AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/server

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata gcc musl-dev \
    && addgroup -S app \
    && adduser -S app -G app \
    && mkdir -p /workspace/.static-checks \
    && chown -R app:app /workspace

COPY --from=builder /out/app /app
COPY --from=builder /usr/local/go /usr/local/go

ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /workspace

USER app
EXPOSE 8080

ENTRYPOINT ["/app"]
