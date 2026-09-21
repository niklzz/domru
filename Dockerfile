# Build the domru proxy from source. Templates are embedded via go:embed.
# The final image is alpine + ffmpeg: the browser player transcodes the
# intercom's MP3 audio to AAC, which iPhones need; without ffmpeg the
# player still works, with MP3 audio (silent on iOS).
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /domru .

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=build /domru /domru
EXPOSE 18000
ENTRYPOINT ["/domru"]
