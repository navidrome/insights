FROM golang:1.23-alpine

RUN apk add --update gcc g++
RUN wget -O- https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b `go env GOPATH`/bin v1.62.0;
RUN go install github.com/cespare/reflex@v0.3.1
RUN go install github.com/onsi/ginkgo/v2/ginkgo@latest

COPY reflex.conf /
CMD ["reflex", "-c", "/reflex.conf"]
