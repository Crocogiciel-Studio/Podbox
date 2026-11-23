---
status: done
---

# Instruction: Kiro image ready for a browser login

Implemented without `KIRO_NO_AUTO_UPDATE` (undocumented; kiro-cli is root-owned in `/usr/local/bin`, so the agent cannot replace it anyway).

## Architecture projection

```txt
.
├── internal/
│   └── images/
│       ├── images.go                  ✏️  base inputs gain podbox-open.sh
│       ├── images_test.go             ✏️
│       └── defs/
│           ├── base.Containerfile     ✏️  install the opener
│           ├── podbox-open.sh         ✅
│           └── kiro.Containerfile     ✏️  pinned version, checksum, DISPLAY, BROWSER
└── internal/agents/agents.go          ✏️  comment: version the flags were confirmed on
```

## Tasks to do

### `1)` URL opener in the base image

1. Create `internal/images/defs/podbox-open.sh`: append `$1` (only the first argument, one line, control characters stripped with `tr -d '\000-\037\177'`) to `/tmp/podbox-open/urls` (directory mode 0700), and print `>>> Open this URL in a browser on your machine:` and the URL on `/dev/tty`, falling back to stderr. Exit 0. It never runs a browser and never reads stdin.
2. In `base.Containerfile`: `COPY podbox-open.sh /usr/local/bin/podbox-open`, `chmod 0755`, `ln -s podbox-open /usr/local/bin/xdg-open` (root-owned, before `USER agent`).
3. In `images.go` `inputs`, add `podbox-open.sh` to the base files so it is part of the base hash.

### `2)` Pinned, checksummed kiro-cli

1. In `kiro.Containerfile`, replace the `latest` download with:

```dockerfile
ARG KIRO_CLI_VERSION=3.5.0
ARG KIRO_CLI_SHA256_AARCH64=ed45f387bca9356d1e767022a7cb504c3fbda20d3881c55c7d771877335ceec0
ARG KIRO_CLI_SHA256_X86_64=7f9bb76921f477125629619450d5942e160942aba9dd8cdc4a66f7678a20c56d
RUN set -e; \
    case "$(uname -m)" in \
      aarch64|arm64) arch=aarch64; sum="$KIRO_CLI_SHA256_AARCH64" ;; \
      x86_64|amd64)  arch=x86_64;  sum="$KIRO_CLI_SHA256_X86_64" ;; \
      *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl --proto '=https' --tlsv1.2 -sSf \
      "https://desktop-release.q.us-east-1.amazonaws.com/${KIRO_CLI_VERSION}/kirocli-${arch}-linux.zip" -o /tmp/kirocli.zip; \
    echo "${sum}  /tmp/kirocli.zip" | sha256sum -c -; \
    unzip -q /tmp/kirocli.zip -d /tmp; \
    install -m 0755 /tmp/kirocli/bin/kiro-cli /usr/local/bin/kiro-cli; \
    rm -rf /tmp/kirocli /tmp/kirocli.zip
ENV DISPLAY=:0 BROWSER=/usr/local/bin/podbox-open KIRO_NO_AUTO_UPDATE=1
```

2. Keep the `# Experimental` comment, updated: login verified or not per phase 0. `KIRO_NO_AUTO_UPDATE` is a name found in the binary's strings, not documented: check that `kiro-cli` does not try to update itself (no denied request to a download host in `podbox policy log --denied`); drop it if it has no effect.
3. In `agents.go`, update the comment to the version `--trust-all-tools` was last checked on.

Bump procedure (documented in a comment above the ARGs):

```sh
curl --proto '=https' --tlsv1.2 -sS https://desktop-release.q.us-east-1.amazonaws.com/latest/manifest.json \
  | python3 -c 'import json,sys; m=json.load(sys.stdin); print(m["version"]); [print(p["architecture"], p["sha256"]) for p in m["packages"] if p["os"]=="linux" and p["fileType"]=="zip" and not p["download"].endswith("-musl.zip")]'
```

### `3)` Tests

1. `images_test.go`: the base build context holds `podbox-open.sh`; changing it changes the base tag (and so the kiro tag).

## Commands

```sh
make lint test
make cross-embed
sudo install dist/podbox-darwin-arm64 /usr/local/bin/podbox
podbox image build kiro --force                         # sha256sum prints "/tmp/kirocli.zip: OK"
podbox stop kiro-brain-system && podbox start kiro-brain-system   # recreate the agent on the new image
podbox exec kiro-brain-system -- kiro-cli --version     # 3.5.0
podbox exec kiro-brain-system -- sh -c 'echo "$DISPLAY $BROWSER"; ls -l /usr/local/bin/xdg-open'
podbox exec kiro-brain-system -- xdg-open 'https://example.org/x' && podbox exec kiro-brain-system -- cat /tmp/podbox-open/urls
```

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | `xdg-open URL` prints the URL and appends it to `/tmp/podbox-open/urls`; the agent cannot write `/usr/local/bin/podbox-open` |
| 2 | the build fails on a checksum mismatch; `kiro-cli --version` is the pinned one; `kiro-cli login` shows "Opening auth portal" without extra env |
| 3 | `make lint test` passes |
