# Stage 1: Build the Go binary
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy dependency files and download modules
COPY go.mod go.sum ./
RUN go mod download

# Copy project source files
COPY . .

# Build a statically linked binary for target OS Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o line-birthday-notifier .

# Stage 2: Build the minimal runtime image
FROM alpine:latest

# Install tzdata (timezone database) and ca-certificates (HTTPS verification)
RUN apk add --no-cache tzdata ca-certificates

# Set container timezone to Asia/Taipei
ENV TZ=Asia/Taipei
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

WORKDIR /app

# Copy the compiled executable from the build stage
COPY --from=builder /app/line-birthday-notifier .

# Expose the application port (default 8080)
EXPOSE 8080

# Run the binary
CMD ["./line-birthday-notifier"]
