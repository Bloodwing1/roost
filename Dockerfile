FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/roost .

FROM scratch
COPY --from=build /out/roost /roost
VOLUME /data
EXPOSE 3000
USER 1000:1000
ENTRYPOINT ["/roost", "-addr", ":3000", "-data", "/data"]
