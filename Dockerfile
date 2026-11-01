# Builds the Go binaries (api, worker, migrate). Select one with --target.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot AS base
COPY config.example.yaml /etc/marketing-research-agent/config.yaml
ENV RESEARCH_AGENT_CONFIG=/etc/marketing-research-agent/config.yaml
USER 65532:65532

FROM base AS api
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]

FROM base AS worker
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/worker"]

# Also carries `seed` for one-off bootstrap jobs (deploy/k8s/jobs/seed.yaml).
FROM base AS migrate
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/seed /usr/local/bin/seed
ENTRYPOINT ["/usr/local/bin/migrate"]
