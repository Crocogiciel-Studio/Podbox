# Experimental: the installer host was unreachable when this was written.
ARG BASE
FROM ${BASE}
USER root
RUN podbox-apt-install unzip
USER agent
RUN curl -fsSL https://cli.kiro.dev/install | bash
# The home is a volume at run time: keep binaries outside it.
USER root
RUN set -e; for f in /home/agent/.local/bin/*; do [ -e "$f" ] && cp -L "$f" /usr/local/bin/; done; rm -rf /home/agent/.local/bin
USER agent
