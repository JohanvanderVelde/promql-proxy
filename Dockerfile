FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /promql-proxy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /promql-proxy /promql-proxy
USER nonroot:nonroot

# FROM alpine:3.20
# RUN addgroup -g 65534 nonroot && adduser -D -u 65534 -G nonroot nonroot
# COPY --from=builder /promql-proxy /promql-proxy
# USER nonroot:nonroot

ENTRYPOINT ["/promql-proxy"]
