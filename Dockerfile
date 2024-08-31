FROM golang:1.23-bookworm AS build

WORKDIR /app
COPY . ./
RUN go mod download
RUN go test -v ./...
RUN CGO_ENABLED=0  go build -trimpath -o sshjump ./cmd/sshjump

FROM gcr.io/distroless/base-debian12 AS run
WORKDIR /
COPY --from=build /app/sshjump /sshjump

USER nonroot:nonroot

CMD ["/sshjump"]
