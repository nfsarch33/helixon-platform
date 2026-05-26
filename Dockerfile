FROM golang:1.23-bookworm AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /helixon ./cmd/helixon

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=builder /helixon /helixon
ENTRYPOINT ["/helixon"]
