FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
RUN microdnf install -y findutils && microdnf clean all
COPY catalog/ /configs
LABEL operators.operatorframework.io.index.configs.v1=/configs
ENTRYPOINT ["/bin/sh", "-c", "exec find /configs -type f -name '*.yaml' -exec cat {} \;"]
CMD ["serve", "/configs"]
