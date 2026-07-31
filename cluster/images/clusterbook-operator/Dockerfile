FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /manager ./cmd

FROM gcr.io/distroless/static@sha256:9197324ba51d9cd071af8505989365c006adf9d6d2067eada25aef00abbb5278

COPY --from=builder /manager /usr/local/bin/clusterbook-operator

USER 65532
ENTRYPOINT ["clusterbook-operator"]
