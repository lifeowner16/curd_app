FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.23
RUN addgroup -S app && adduser -S -G app app && mkdir /data && chown app:app /data
COPY --from=build /out/api /usr/local/bin/api
USER app
ENV HTTP_ADDR=0.0.0.0:8080 DB_PATH=/data/app.db
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/api"]
