FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /manager ./cmd

FROM gcr.io/distroless/static@sha256:1a64f46364620465ef9e9a8f5413757dfdf22695e050bbfd7303eeaf17a59383

COPY --from=builder /manager /usr/local/bin/clusterbook-operator

USER 65532
ENTRYPOINT ["clusterbook-operator"]
