# Build: docker compose build, or docker build -t hhg-attendance .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# timetzdata embeds the zone database: the runtime image has none. Listen on all interfaces inside the container
# so the reverse proxy on the Docker network can reach it; compose publishes it on the host's loopback only.
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata \
    -ldflags "-s -w -X main.dbDriver=mysql -X main.webAddr=:8080" \
    -o /out/hhg-attendance .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hhg-attendance /hhg-attendance
EXPOSE 8080 7600/udp
ENTRYPOINT ["/hhg-attendance"]
