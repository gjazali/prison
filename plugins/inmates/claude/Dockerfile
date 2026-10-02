# `prison build` supplies `PRISON_BASE`, so this layer cannot replace the
# entrypoint that installs the network rules.

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

# Shims for pnpm and yarn so that `prison up` can install projects with them.
# They are in this layer because this layer installs Node.
RUN if command -v corepack >/dev/null 2>&1; then corepack enable; fi

ARG CLAUDE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
  && npm cache clean --force
