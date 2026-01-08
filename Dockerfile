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

WORKDIR /app
COPY --from=builder /app/feedback /app/feedback
# Copy OpenAPI spec next to the binary so the running executable can find it
COPY --from=builder /app/openapi.yaml /app/openapi.yaml

ENTRYPOINT ["/app/feedback"]
