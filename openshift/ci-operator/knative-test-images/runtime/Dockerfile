# Do not edit! This file was generated via Makefile
FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.21-builder-multi-openshift-4.16 as builder

COPY . .
RUN make install test-install

FROM registry.access.redhat.com/ubi8/ubi-minimal
USER 65532

COPY --from=builder /go/bin/runtime /ko-app/runtime
ENTRYPOINT ["/ko-app/runtime"]
