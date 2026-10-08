# syntax=docker/dockerfile:1

FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The release tag, stamped in because the build context has no .git.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /bootleg-mcp ./cmd/bootleg-mcp

# Static binary, CA certificates, no shell, non-root.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /bootleg-mcp /bootleg-mcp
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/bootleg-mcp"]
