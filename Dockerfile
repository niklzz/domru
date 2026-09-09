# Build the domru proxy from source. Templates are embedded via go:embed,
# so the final image is just the static binary on distroless.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /domru .

FROM gcr.io/distroless/static-debian12
COPY --from=build /domru /domru
EXPOSE 18000
ENTRYPOINT ["/domru"]
