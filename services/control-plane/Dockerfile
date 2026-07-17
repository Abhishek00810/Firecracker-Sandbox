FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane

FROM alpine:3.21

RUN apk --no-cache add ca-certificates && adduser -D -H -u 10001 controlplane
USER controlplane

COPY --from=build /out/control-plane /usr/local/bin/control-plane

EXPOSE 8081
ENTRYPOINT ["control-plane"]
