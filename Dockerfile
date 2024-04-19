FROM registry.suse.com/bci/golang:1.22 as builder

WORKDIR /app

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build


# final stage
FROM registry.suse.com/bci/bci-micro:15.5
COPY --from=builder /app/feedback /app/

EXPOSE 3000
ENTRYPOINT ["/app/feedback"]

