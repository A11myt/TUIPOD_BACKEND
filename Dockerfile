FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o tuipod ./cmd/server
RUN CGO_ENABLED=0 go build -o tuipod-worker ./cmd/worker

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/tuipod .
COPY --from=builder /app/tuipod-worker .
COPY migrations ./migrations
EXPOSE 8080
# default: API server. The worker service overrides this command (see docker-compose.yml).
CMD ["./tuipod"]
