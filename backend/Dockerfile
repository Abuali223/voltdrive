# ---- build stage ----
FROM golang:1.24-alpine AS build
WORKDIR /src
# Dependency layer (cached unless go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download
# Source.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ---- run stage ----
FROM gcr.io/distroless/static-debian12
COPY --from=build /bin/server /server
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
