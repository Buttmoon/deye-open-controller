FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /app/inverter-schedule .

FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser && \
    mkdir -p /app/data /app/device_parameters && \
    chown -R appuser:appuser /app

COPY --from=builder /app/inverter-schedule /app/inverter-schedule
COPY --from=builder /app/inverter_models.json /app/inverter_models.json
COPY --from=builder /app/device_parameters /app/device_parameters
# Старый файл оставлен для обратной совместимости и сравнения миграции.
COPY --from=builder /app/device_parameters.json /app/device_parameters.json

USER appuser

EXPOSE 8080
ENV TZ=UTC

CMD ["/app/inverter-schedule"]
