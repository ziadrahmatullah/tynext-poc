# Satu image untuk ingest dan simulator: keduanya memakai kontrak yang sama,
# jadi keduanya harus dibangun dari pohon sumber yang sama juga.
FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ingest    ./cmd/ingest \
 && CGO_ENABLED=0 go build -trimpath -o /out/simulator ./cmd/simulator

FROM alpine:3.20

RUN adduser -D -u 10001 rain
COPY --from=build /out/ingest    /usr/local/bin/ingest
COPY --from=build /out/simulator /usr/local/bin/simulator

# Direktori buffer dibuat di dalam image supaya named volume yang di-mount ke
# sini mewarisi kepemilikan rain, bukan root.
RUN mkdir -p /home/rain/var && chown -R rain:rain /home/rain

USER rain
WORKDIR /home/rain
ENTRYPOINT ["/usr/local/bin/ingest"]
