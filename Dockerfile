FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build -o application ./main.go


FROM alpine:3.22

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/application /application

ENTRYPOINT ["/application"]
