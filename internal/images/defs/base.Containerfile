# Debian-based image; override with images.base in config.yaml.
ARG BASE=docker.io/library/node:22-bookworm-slim
FROM ${BASE}
USER root
COPY ca/ /usr/local/share/ca-certificates/podbox/
COPY apt-install.sh /usr/local/bin/podbox-apt-install
RUN chmod 0755 /usr/local/bin/podbox-apt-install \
 && podbox-apt-install bash ca-certificates curl git less procps
# Exactly one user owns uid 1000: agent, home /home/agent.
RUN set -e; \
    if u=$(getent passwd 1000 | cut -d: -f1) && [ -n "$u" ]; then userdel -r "$u" 2>/dev/null || userdel "$u"; fi; \
    if g=$(getent group 1000 | cut -d: -f1) && [ -n "$g" ]; then groupdel "$g"; fi; \
    groupadd -g 1000 agent; \
    useradd -m -u 1000 -g 1000 -s /bin/bash agent
RUN set -e; update-ca-certificates; \
    if command -v keytool >/dev/null; then \
      for c in /usr/local/share/ca-certificates/podbox/*.crt; do \
        if [ -f "$c" ]; then keytool -importcert -noprompt -cacerts -storepass changeit -alias "podbox-$(basename "$c")" -file "$c"; fi; \
      done; \
    fi
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
COPY entrypoint.sh /usr/local/bin/podbox-entrypoint
RUN chmod 0755 /usr/local/bin/podbox-entrypoint
USER agent
WORKDIR /home/agent
ENTRYPOINT ["/usr/local/bin/podbox-entrypoint"]
CMD ["podbox-idle"]
