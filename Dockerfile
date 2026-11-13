FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web/ ./web/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chat-screenshots .
RUN install -d -m 0700 -o 65532 -g 65532 /data/images && chown 65532:65532 /data && chmod 0700 /data

FROM scratch
COPY --from=build /out/chat-screenshots /chat-screenshots
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/chat-screenshots"]
CMD ["-data", "/data"]
