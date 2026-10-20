# Build: docker compose build (reads TIMEZONE from .env), or
#   docker build --build-arg TIMEZONE=Asia/Jakarta -t hhg-attendance .
FROM golang:1.27-alpine AS build
ARG TIMEZONE
RUN test -n "$TIMEZONE" || (echo "set the TIMEZONE build arg" >&2; exit 1)
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# timetzdata embeds the zone database: the runtime image has none. Listen on all interfaces inside the container
# so the reverse proxy on the Docker network can reach it; compose publishes it on the host's loopback only.
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata \
    -ldflags "-s -w -X main.timezone=${TIMEZONE} -X main.dbDriver=mysql -X main.webAddr=:8080" \
    -o /out/hhg-attendance .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hhg-attendance /hhg-attendance
EXPOSE 8080 7600/udp
ENTRYPOINT ["/hhg-attendance"]
