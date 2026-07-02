# Used by goreleaser: the prebuilt binary for the target platform is copied
# into the build context, so this file must not compile anything.
FROM gcr.io/distroless/static-debian12

COPY fileferry /fileferry

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["/fileferry", "--addr", ":8080", "--data-dir", "/data"]
