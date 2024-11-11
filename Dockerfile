FROM --platform=linux/amd64 golang:1.23 AS build

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ENV CGO_ENABLED=1
RUN go build -o /insights

FROM --platform=linux/amd64 debian:bookworm AS final
COPY --from=build /insights /insights
WORKDIR /app
CMD ["/insights"]