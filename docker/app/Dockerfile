FROM public.ecr.aws/docker/library/golang:1.26-alpine

RUN apk add --update gcc g++
RUN wget -O- https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.12.2
RUN go install github.com/cespare/reflex@v0.3.1
RUN go install github.com/onsi/ginkgo/v2/ginkgo@latest

COPY reflex.conf /
CMD ["reflex", "-c", "/reflex.conf"]
