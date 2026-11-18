FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/statusgrid-server ./cmd/statusgrid-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/statusgrid-probe ./cmd/statusgrid-probe

FROM alpine:3.21
RUN adduser -D -H -u 10001 statusgrid
WORKDIR /app
COPY --from=build /out/statusgrid-server /usr/local/bin/statusgrid-server
COPY --from=build /out/statusgrid-probe /usr/local/bin/statusgrid-probe
RUN mkdir -p /data && chown statusgrid:statusgrid /data
USER statusgrid
EXPOSE 8094
ENV STATUSGRID_ADDR=0.0.0.0:8094
ENV STATUSGRID_DATA_DIR=/data
ENTRYPOINT ["statusgrid-server"]
