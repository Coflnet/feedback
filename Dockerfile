FROM golang:1.21.0-bookworm as builder

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
RUN go build .

FROM gcr.io/distroless/static-debian11

COPY --from=builder /app/feedback /feedback
CMD ["/feedback"]