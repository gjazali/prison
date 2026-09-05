# The Claude Code layer of a prison box.
#
# Layered on whatever prison built before it, which is at least
# `images/base` and its guest agent. `PRISON_BASE` is supplied by
# `prison build`, so this file names no image of its own and cannot replace
# the entrypoint that installs the network rules.

ARG PRISON_BASE
FROM ${PRISON_BASE}

ARG NODE_MAJOR=24
RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
      -o /usr/share/keyrings/nodesource.asc \
  && echo "deb [signed-by=/usr/share/keyrings/nodesource.asc]" \
      "https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" \
      > /etc/apt/sources.list.d/nodesource.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends nodejs \
  && rm -f /etc/apt/sources.list.d/nodesource.list \
      /usr/share/keyrings/nodesource.asc \
  && rm -rf /var/lib/apt/lists/*

# Shims for pnpm and yarn, so `prison up` can install a project using
# either. They sit in this layer because this is what brings Node. The
# guard covers a Node that no longer ships corepack.
RUN if command -v corepack >/dev/null 2>&1; then corepack enable; fi

ARG CLAUDE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
  && npm cache clean --force
