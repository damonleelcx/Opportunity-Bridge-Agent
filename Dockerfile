# 阿桥 · Opportunity Bridge Agent
#
# Packaging only: the binary is compiled on the build host by deploy/build.sh and
# copied in. That is deliberate.
#
# Compiling inside the image would be more self-contained on paper, but it puts a
# module download on the critical path of every deploy, which fails on any build
# daemon without outbound network — including the local VM this is built on. The
# Go toolchain already produces a fully static, reproducible binary for the
# target architecture; the image adds a filesystem and a user, and nothing else.
#
# The interface is embedded in the binary (web/embed.go), so there is no asset
# volume and nothing that can get out of step with the server serving it. The
# corpus is copied in rather than embedded, because replacing it with real feeds
# should not mean recompiling.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY dist/obagent /app/obagent
COPY data /app/data

ENV OBA_ADDR=:8787 \
    OBA_CORPUS_DIR=/app/data \
    OBA_STATE_PATH=/app/state/oba.json
EXPOSE 8787
USER nonroot:nonroot
ENTRYPOINT ["/app/obagent"]
