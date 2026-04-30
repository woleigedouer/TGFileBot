# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src

RUN apk --no-cache add ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/TGBot .

FROM alpine:3.20

WORKDIR /root

RUN apk --no-cache add ca-certificates tzdata && mkdir -p files

COPY --from=builder /out/TGBot ./TGBot

RUN chmod +x ./TGBot

ENV TZ=Asia/Shanghai
ENV LOG=""

ENTRYPOINT ["/bin/sh", "-c", "./TGBot -files ./files ${LOG:+-log $LOG} \"$@\"", "--"]
