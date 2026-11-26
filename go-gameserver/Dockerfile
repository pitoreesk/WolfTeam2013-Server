FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wtserver ./cmd/wtserver
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wtadmin ./cmd/wtadmin

FROM alpine:3.22

RUN addgroup -S gameserver && adduser -S -G gameserver gameserver
WORKDIR /app
COPY --from=build /out/wtserver /app/wtserver
COPY --from=build /out/wtadmin /app/wtadmin
COPY patches /app/patches
COPY LICENSES /licenses
USER gameserver

EXPOSE 8080/tcp 8444/tcp 40706/tcp 40707/tcp 40707/udp 40708/udp 40709/tcp 40710/udp 41000-41499/udp
ENTRYPOINT ["/app/wtserver"]
