FROM --platform=linux/amd64 golang:1.26.4-alpine AS build

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/localserver ./localserver

FROM --platform=linux/amd64 alpine:3.22

WORKDIR /app
COPY --from=build /out/localserver ./localserver
COPY --from=build /src/site ./site

EXPOSE 8080
CMD ["./localserver"]
