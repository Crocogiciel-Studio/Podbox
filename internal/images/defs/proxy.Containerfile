FROM scratch
COPY podbox /podbox
USER 65534:65534
EXPOSE 3128
ENTRYPOINT ["/podbox", "proxy", "serve"]
CMD ["--listen", ":3128", "--policy", "/etc/podbox/policy.effective.json"]
