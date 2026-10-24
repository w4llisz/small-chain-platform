FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/small-chain ./cmd/small-chain && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/small-chain-worker ./cmd/small-chain-worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

FROM scratch
COPY --from=build /out/ /
USER 65532:65532
EXPOSE 8080 9091
ENTRYPOINT ["/small-chain"]
CMD ["-addr=0.0.0.0:8080"]
