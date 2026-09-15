FROM quay.io/operator-framework/opm:latest@sha256:b32d3891616662620da08d7f0ec42c2e69fa2de43427dc975d35b12f7a969a0f AS builder
COPY catalog/ /configs
RUN ["/bin/opm", "serve", "/configs", "--cache-dir=/tmp/cache", "--cache-only"]

FROM quay.io/operator-framework/opm:latest@sha256:b32d3891616662620da08d7f0ec42c2e69fa2de43427dc975d35b12f7a969a0f
COPY --from=builder /configs /configs
COPY --chown=65532:65532 --from=builder /tmp/cache /tmp/cache
EXPOSE 50051
USER 65532:65532
ENTRYPOINT ["/bin/opm"]
CMD ["serve", "/configs", "--cache-dir=/tmp/cache"]
LABEL operators.operatorframework.io.index.configs.v1=/configs
