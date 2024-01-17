FROM registry.suse.com/bci/golang:1.21 as builder

WORKDIR /app
# ENV GO111MODULE=on

COPY go.mod .
COPY go.sum .

RUN go mod download

# FROM build_base AS server_builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build


# final stage
FROM registry.suse.com/bci/bci-micro:latest
COPY --from=builder /app/feedback /app/

EXPOSE 3000
ENTRYPOINT ["/app/feedback"]

