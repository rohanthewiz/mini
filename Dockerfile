# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS builder
WORKDIR /work

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_NUMBER=""
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.BuildNumber=${BUILD_NUMBER}" -o app .


# Alternative distroless runtime (smaller, no shell):
# FROM gcr.io/distroless/static-debian12
FROM alpine:3.20

RUN apk add --no-cache ca-certificates && \
    addgroup -g 1001 -S app && \
    adduser -u 1001 -S app -G app

COPY --from=builder --chmod=0755 --chown=1001:1001 /work/app /app/app

USER 1001:1001
WORKDIR /app

EXPOSE 8000
ENTRYPOINT [ "./app" ]