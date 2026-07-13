# Multi-stage build: static pdbq binary on distroless.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/pdbq ./cmd/pdbq

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pdbq /pdbq
EXPOSE 8080
# Distroless has no shell; healthchecks are performed by the orchestrator
# against GET /healthz.
ENTRYPOINT ["/pdbq"]
CMD ["serve"]
