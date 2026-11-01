# Stage 1: Build Go binary
FROM golang:1.26-bookworm AS builder

WORKDIR /src
ENV GOTOOLCHAIN=auto

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server .

# Stage 2: Runtime image with SingleFile (Node.js + Chromium) and ffmpeg
FROM debian:bookworm-slim

WORKDIR /app
ENV PORT=8080 \
    DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        nodejs \
        npm \
        chromium \
        ffmpeg \
    && npm install -g single-file-cli \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/server /app/server
COPY static /app/static

EXPOSE 8080
ENTRYPOINT ["/app/server"]
