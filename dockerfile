FROM golang:1.26 AS build

WORKDIR /app
COPY go.mod ./
COPY . .

# Compila os três executáveis.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/motorista ./cmd/client/motorista
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/passageiro ./cmd/client/passageiro


FROM alpine:3.21 AS runtime-base
RUN apk add --no-cache ca-certificates
WORKDIR /app


FROM runtime-base AS server
COPY --from=build /out/server /app/server
EXPOSE 9593 8081
ENTRYPOINT ["/app/server"]


FROM runtime-base AS motorista
COPY --from=build /out/motorista /app/motorista
ENTRYPOINT ["/app/motorista"]


FROM runtime-base AS passageiro
COPY --from=build /out/passageiro /app/passageiro
ENTRYPOINT ["/app/passageiro"]