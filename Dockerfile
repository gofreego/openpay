# Stage 1: build the admin console (ui/).
FROM node:22-alpine AS ui-builder

WORKDIR /app/ui

COPY ui/package.json ui/package-lock.json ./
RUN npm ci

# Vite bakes these into the bundle at build time. Defaults are production;
# override with --build-arg for another environment.
ARG VITE_API_BASE_URL=https://api.bappaapp.com
ARG VITE_LOGIN_URL=https://admin.bappaapp.com/openauth/admin/v2/login
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL \
    VITE_LOGIN_URL=$VITE_LOGIN_URL

COPY ui/ ./
RUN npm run build

# Stage 2: build the Go binary, embedding the console (main.go: go:embed ui/dist).
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Replace the committed placeholder with the built console.
COPY --from=ui-builder /app/ui/dist ./ui/dist

RUN CGO_ENABLED=0 GOOS=linux go build -o application .

# Stage 3: run. The console is served at /payments/, next to the API.
FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add ca-certificates

COPY --from=builder /app/application .
COPY dev.yaml .
COPY api/docs /app/api/docs

RUN chmod +x application

# 8085 HTTP (API, console, webhooks), 8086 gRPC.
EXPOSE 8085 8086

CMD [ "/app/application" ]
