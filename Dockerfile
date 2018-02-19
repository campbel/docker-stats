FROM golang:1.9.2-alpine3.6 as builder
ADD src /go/src
RUN go install agent

FROM alpine:3.6
RUN apk update && apk add --no-cache ca-certificates curl
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=3 CMD curl -f http://localhost/health || exit 1
COPY --from=builder /go/bin/agent .
CMD ["./agent"]
