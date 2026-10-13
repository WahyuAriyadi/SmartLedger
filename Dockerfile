# syntax=docker/dockerfile:1
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
# `go mod tidy` membuat go.sum bila belum ada (tidak perlu Go terpasang di Windows).
# Setelah menjalankan `go mod tidy` secara lokal dan meng-commit go.sum, langkah ini tidak mengubah apa pun.
RUN go mod tidy && go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger ./cmd/api
RUN mkdir -p /out/data/receipts

# Image akhir: tanpa shell, tanpa package manager, berjalan sebagai non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ledger /ledger
# Folder foto nota dimiliki user nonroot (uid 65532) agar volume bisa ditulis tanpa root.
COPY --from=build --chown=65532:65532 /out/data /data
ENV STORAGE_LOCAL_DIR=/data/receipts
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ledger"]
