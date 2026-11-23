# Experimental: kiro-cli is closed source and its flags change between releases.
ARG BASE
FROM ${BASE}
USER root
RUN podbox-apt-install unzip
# kiro-cli is pinned and checked, so a rebuild never changes it silently.
# To bump, read the version and the linux zip checksums (not the -musl ones) from:
#   curl -sS https://desktop-release.q.us-east-1.amazonaws.com/latest/manifest.json
# and update the three values below.
ARG KIRO_CLI_VERSION=3.5.0
ARG KIRO_CLI_SHA256_AARCH64=ed45f387bca9356d1e767022a7cb504c3fbda20d3881c55c7d771877335ceec0
ARG KIRO_CLI_SHA256_X86_64=7f9bb76921f477125629619450d5942e160942aba9dd8cdc4a66f7678a20c56d
# Installed outside the agent home, which is a volume at run time.
RUN set -e; \
    case "$(uname -m)" in \
      aarch64|arm64) arch=aarch64; sum="$KIRO_CLI_SHA256_AARCH64" ;; \
      x86_64|amd64)  arch=x86_64;  sum="$KIRO_CLI_SHA256_X86_64" ;; \
      *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl --proto '=https' --tlsv1.2 -sSf \
      "https://desktop-release.q.us-east-1.amazonaws.com/${KIRO_CLI_VERSION}/kirocli-${arch}-linux.zip" \
      -o /tmp/kirocli.zip; \
    echo "${sum}  /tmp/kirocli.zip" | sha256sum -c -; \
    unzip -q /tmp/kirocli.zip -d /tmp; \
    install -m 0755 /tmp/kirocli/bin/kiro-cli /usr/local/bin/kiro-cli; \
    rm -rf /tmp/kirocli /tmp/kirocli.zip
# kiro-cli takes a session without a display for a remote machine and offers
# only the device flow, which organizations signing in through Entra ID or
# Okta do not have. With a display it uses the browser flow; the browser is
# podbox-open, and podbox auth login relays the callback.
ENV DISPLAY=:0 BROWSER=/usr/local/bin/podbox-open
USER agent
