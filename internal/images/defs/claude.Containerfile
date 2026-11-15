ARG BASE
FROM ${BASE}
USER root
RUN npm install -g @anthropic-ai/claude-code && npm cache clean --force
ENV DISABLE_AUTOUPDATER=1
USER agent
