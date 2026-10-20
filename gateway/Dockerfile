FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mkapp ./cmd/mkapp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /usr/local/bin/gateway
COPY --from=build /out/mkapp /usr/local/bin/mkapp
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gateway"]
