FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /small-chain ./cmd/small-chain

FROM scratch
COPY --from=build /small-chain /small-chain
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/small-chain"]
CMD ["-addr=0.0.0.0:8080"]
