FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/guard ./cmd/guard

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/guard /usr/local/bin/guard
ENTRYPOINT ["/usr/local/bin/guard"]
