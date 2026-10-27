# syntax=docker/dockerfile:1
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
# `go mod tidy` creates go.sum if missing (no local Go install needed on Windows).
# Once go.sum is generated locally and committed, this step changes nothing.
RUN go mod tidy && go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger ./cmd/api
RUN mkdir -p /out/data/receipts

# Final image: no shell, no package manager, runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ledger /ledger
# The receipt photo folder is owned by the nonroot user (uid 65532) so the volume is writable without root.
COPY --from=build --chown=65532:65532 /out/data /data
ENV STORAGE_LOCAL_DIR=/data/receipts
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ledger"]
