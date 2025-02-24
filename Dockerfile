# Build stage
FROM golang:1.24 AS builder
WORKDIR /app
COPY . .
# Only run go mod init if go.mod doesn't exist, then tidy up
RUN go mod init taxscraper || true
RUN go mod tidy
# Build with optimizations for a static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o taxscraper

# Run stage
FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/taxscraper .
# Optional: document the default port, though Cloud Run uses PORT env var
EXPOSE 8080
# Run the binary
CMD ["./taxscraper"]