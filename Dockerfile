FROM golang:1.8 as builder
WORKDIR /go/src/github.com/crewjam/triggr

RUN go get -v github.com/BurntSushi/toml
RUN go get -v github.com/crewjam/httperr
RUN go get -v github.com/crewjam/errset
RUN go get -v github.com/golang/glog
RUN go get -v github.com/google/go-github/github
RUN go get -v github.com/kr/pretty
RUN go get -v goji.io
RUN go get -v goji.io/pat
RUN go get -v golang.org/x/oauth2
RUN go get -v k8s.io/api/core/v1
RUN go get -v k8s.io/apimachinery/pkg/apis/meta/v1
RUN go get -v k8s.io/apimachinery/pkg/fields
RUN go get -v k8s.io/apimachinery/pkg/util/runtime
RUN go get -v k8s.io/apimachinery/pkg/util/wait
RUN go get -v k8s.io/client-go/kubernetes
RUN go get -v k8s.io/client-go/plugin/pkg/client/auth/gcp
RUN go get -v k8s.io/client-go/tools/cache
RUN go get -v k8s.io/client-go/tools/clientcmd
RUN go get -v k8s.io/client-go/util/workqueue

COPY . .
RUN go build -o /triggr .

FROM alpine:latest
RUN mkdir /lib64 && ln -s /lib/libc.musl-x86_64.so.1 /lib64/ld-linux-x86-64.so.2 # http://stackoverflow.com/a/35613430
COPY --from=builder /triggr /usr/bin/triggr
CMD ["/usr/bin/triggr"]
