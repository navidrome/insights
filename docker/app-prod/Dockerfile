FROM public.ecr.aws/docker/library/golang:1.25 AS build

WORKDIR /workspace
RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

ENV CGO_ENABLED=1

RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /insights ./cmd/server

RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /monitor ./cmd/monitor

FROM scratch AS binary
COPY --from=build /insights /insights

FROM public.ecr.aws/docker/library/debian:bookworm AS final
COPY --from=build /insights /insights
COPY --from=build /monitor /monitor
WORKDIR /app
CMD ["/insights"]