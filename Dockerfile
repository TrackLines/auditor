FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /auditor ./cmd/auditor

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /auditor /auditor
EXPOSE 8080
ENV HTTP_PORT=8080
ENTRYPOINT ["/auditor"]
