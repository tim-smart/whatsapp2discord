FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY *.go ./
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /whatsapp2discord .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /whatsapp2discord /whatsapp2discord
EXPOSE 8080
ENTRYPOINT ["/whatsapp2discord"]
