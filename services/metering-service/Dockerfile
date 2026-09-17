# Base images are pinned by digest, not tag — a tag can be re-pushed, and this

FROM golang@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS build

WORKDIR /src


COPY go.mod go.sum ./
RUN go mod download

COPY . .

# A build arg, never a runtime env var
ARG VERSION=dev


RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/service .


FROM gcr.io/distroless/static-debian12@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

COPY --from=build /out/service /service


EXPOSE 8081


ENTRYPOINT ["/service"]
