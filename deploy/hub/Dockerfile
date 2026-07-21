# hubd server image. Multi-stage: static build → distroless nonroot.
# Build context is the REPO ROOT (single module): docker build -f deploy/hub/Dockerfile .
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hubd ./cmd/hubd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hubd /hubd
EXPOSE 8080
# /healthz is dependency-free liveness; the platform readiness probe should hit
# /readyz. Config is injected at deploy (no baked-in endpoints).
ENTRYPOINT ["/hubd"]
