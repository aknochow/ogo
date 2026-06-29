FROM quay.io/operator-framework/opm:latest AS builder
COPY catalog/ /configs
RUN ["/bin/opm", "serve", "/configs", "--cache-dir=/tmp/cache", "--cache-only"]

FROM quay.io/operator-framework/opm:latest
COPY --from=builder /configs /configs
COPY --from=builder /tmp/cache /tmp/cache
EXPOSE 50051
USER 65532:65532
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
LABEL operators.operatorframework.io.index.configs.v1=/configs
