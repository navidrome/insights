FROM public.ecr.aws/docker/library/golang:1.26 AS build

WORKDIR /workspace
RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

ENV CGO_ENABLED=0

RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /ingest ./cmd/ingest

RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /process ./cmd/process

RUN --mount=type=bind,source=. \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /monitor ./cmd/monitor

FROM scratch AS binary
COPY --from=build /ingest /ingest
COPY --from=build /process /process

FROM scratch AS final
COPY --from=build /ingest /ingest
COPY --from=build /process /process
COPY --from=build /monitor /monitor
WORKDIR /app
CMD ["/ingest"]
