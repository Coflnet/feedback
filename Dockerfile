FROM cgr.dev/chainguard/go:latest-dev as builder

# Set the working directory
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Authenticate with GitHub to access the private repo
ARG token
RUN go mod download

COPY . .
RUN go build -o main .

FROM cgr.dev/chainguard/static:latest

COPY --from=builder /app/main /bin/feedback

CMD ["/bin/feedback"]
