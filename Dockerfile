FROM node:24-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run check && npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /easydist .

FROM alpine:3.23
RUN addgroup -g 10001 easydist && adduser -D -u 10001 -G easydist easydist \
    && mkdir /data /deploy && chown easydist:easydist /data /deploy
COPY --from=backend /easydist /usr/local/bin/easydist
COPY --from=frontend /src/web/build /app/web
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["easydist", "--healthcheck"]
ENTRYPOINT ["easydist"]
