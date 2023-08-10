FROM golang:1.21.0-bookworm as builder

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
RUN go build .

FROM alpine:3.18

COPY --from=builder /app/feedback /usr/local/bin/feedback

ENTRYPOINT ["/usr/local/bin/feedback"]
