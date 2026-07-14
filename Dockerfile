# BubbleClip — Go build: single static binary, UI embedded, image ≈ 8 MB.
# (Node alternative available in Dockerfile.node.)

# ---- stage 1: compile ----
FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod main.go ./
COPY public ./public

# go mod tidy resolves + verifies deps (gorilla/websocket only) during build,
# so the repo doesn't need a committed go.sum
RUN go mod tidy \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bubbleclip . \
 && mkdir -p /out/data \
 && chown -R 1000:1000 /out

# ---- stage 2: empty base, nothing but the binary ----
FROM scratch

COPY --from=build /out/bubbleclip /bubbleclip
# uid 1000 matches the Node image's "node" user, so existing data volumes carry over
COPY --from=build --chown=1000:1000 /out/data /app/data

ENV PORT=5678 \
    DATA_FILE=/app/data/clipboard.json

USER 1000:1000
EXPOSE 5678
VOLUME ["/app/data"]

# the binary doubles as its own healthcheck probe (no shell in scratch)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD ["/bubbleclip", "-health"]

ENTRYPOINT ["/bubbleclip"]
