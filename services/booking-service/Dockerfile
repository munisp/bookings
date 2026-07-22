# --- build stage ---------------------------------------------------------------
FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates

# Cache modules first for layer reuse.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/booking-service ./cmd/server

# --- runtime stage -------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/booking-service /app/booking-service
EXPOSE 7002
USER nonroot:nonroot
ENTRYPOINT ["/app/booking-service"]
