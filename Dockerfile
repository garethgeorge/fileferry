# Used by goreleaser: the prebuilt binary for the target platform is copied
# into the build context, so this file must not compile anything.
FROM gcr.io/distroless/static-debian12

COPY ferryserver /ferryserver

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["/ferryserver", "--addr", ":8080", "--data-dir", "/data"]
