FROM cgr.dev/chainguard/go:latest-dev as builder

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Set the working directory
WORKDIR /app

COPY ./go.mod ./go.sum .
RUN go mod download

COPY . .
RUN go build -o feedback .

FROM cgr.dev/chainguard/static

COPY --from=builder /app/feedback /usr/bin/feedback

ENTRYPOINT ["/usr/bin/feedback"]
