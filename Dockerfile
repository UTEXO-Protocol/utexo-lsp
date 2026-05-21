FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o application ./main.go

FROM alpine:3.22 AS runtime

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/application /application

ENTRYPOINT ["/application"]
