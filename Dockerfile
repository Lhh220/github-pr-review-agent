FROM golang:1.25.0-alpine3.22 AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/server

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S app -G app

COPY --from=builder /out/app /usr/local/bin/app

USER app
EXPOSE 8080

ENTRYPOINT ["app"]
