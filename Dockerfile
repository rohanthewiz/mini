# syntax=docker/dockerfile:1
FROM cgr.dev/chainguard/go:latest-dev AS builder
WORKDIR /work

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_NUMBER=""
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.BuildNumber=${BUILD_NUMBER}" -o app .


FROM cgr.dev/chainguard/static:latest

COPY --from=builder --chmod=0755 --chown=1001:1001 /work/app /app/app

USER 1001:1001
WORKDIR /app

EXPOSE 8000
ENTRYPOINT [ "./app" ]