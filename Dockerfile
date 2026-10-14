# Multi-stage build: compila o govpn-server em um estágio com toolchain
# Go completo, depois copia só o binário final para uma imagem mínima.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY common/ ./common/
COPY server/ ./server/
RUN CGO_ENABLED=0 go build -o /out/govpn-server ./server/

FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/govpn-server /usr/local/bin/govpn-server
VOLUME ["/data"]
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/govpn-server"]
CMD ["serve", "--db", "/data/govpn.db", "--addr", ":8443"]
