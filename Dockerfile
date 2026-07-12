# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -o paste -ldflags="-s -w -X main.version=${VERSION}" .

# Final stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates

COPY --from=builder /build/paste /paste

ENV PORT=8080
ENV DATA_DIR=/data
ENV BASE_URL=http://localhost:8080
ENV MAX_UPLOAD_MB=2048

RUN mkdir -p /data

EXPOSE 8080

CMD ["/paste"]
