# Stage 1: Build proofreading UI
FROM node:20-alpine AS ui-builder

WORKDIR /app
COPY proofreading/package.json proofreading/package-lock.json* ./
RUN npm install --frozen-lockfile 2>/dev/null || npm install

COPY proofreading/ .
RUN npm run build

# Stage 2: Build Go backend
FROM golang:1.23-alpine AS go-builder

WORKDIR /src
COPY go.mod ./
RUN go mod download 2>/dev/null || true

COPY main.go ./
COPY backend/ ./backend/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /sekai-translate .

# Stage 3: Minimal runtime
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=go-builder /sekai-translate ./sekai-translate
COPY --from=ui-builder /app/out/ ./proofreading-ui/
COPY translations/ ./translations/

ENV PORT=9090
ENV TRANSLATION_PATH=/app/translations
ENV STATIC_DIR=/app/proofreading-ui

EXPOSE 9090

CMD ["./sekai-translate"]
