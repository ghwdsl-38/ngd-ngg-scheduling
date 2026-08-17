FROM scratch
COPY build/prc /prc
USER 65532:65532
ENTRYPOINT ["/prc"]
