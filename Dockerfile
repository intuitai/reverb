FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /reverb ./cmd/reverb

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
COPY --from=builder /reverb /usr/local/bin/reverb
EXPOSE 8080 9090 9091 9100
ENTRYPOINT ["reverb"]
CMD ["--http-addr", ":8080"]
