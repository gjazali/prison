# Base image for every prison box. Inmate images build on it through
# `ARG PRISON_BASE`.

ARG PRISON_FOUNDATION=debian:bookworm-slim
FROM ${PRISON_FOUNDATION}

# `ncurses-term` adds terminal descriptions that Debian leaves out. The guest
# agent uses `iptables` and `iproute2` for confinement.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      fd-find \
      git \
      iproute2 \
      iptables \
      jq \
      less \
      ncurses-term \
      procps \
      ripgrep \
      sudo \
      xz-utils \
  && rm -rf /var/lib/apt/lists/* \
  && ln -sf "$(which fdfind)" /usr/local/bin/fd

# Shims for pnpm and yarn so that `prison up` can install projects with them.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN if command -v corepack >/dev/null 2>&1; then corepack enable; fi

# Universal runtime version manager (`mise`). Data, config, cache, and state is
# kept under `/home/dev/.local/share/mise`.
ARG MISE_VERSION=latest
ARG MISE_RELEASES=https://api.github.com/repos/jdx/mise/releases
RUN set -eu \
  && case "$(dpkg --print-architecture)" in \
       amd64) architecture=x64 ;; \
       arm64) architecture=arm64 ;; \
       *) echo "unsupported architecture" >&2; exit 1 ;; \
     esac \
  && if [ "${MISE_VERSION}" = latest ]; then \
       release_path=latest; \
     else \
       release_path="tags/v${MISE_VERSION#v}"; \
     fi \
  && release="$(curl -fsSL "${MISE_RELEASES}/${release_path}")" \
  && tag="$(printf '%s' "${release}" | jq -er .tag_name)" \
  && asset="mise-${tag}-linux-${architecture}.tar.gz" \
  && download="$(printf '%s' "${release}" | jq -er --arg name "${asset}" \
       '.assets[] | select(.name == $name) | .browser_download_url')" \
  && sums_url="$(printf '%s' "${release}" | jq -er \
       '.assets[] | select(.name == "SHASUMS256.txt") | .browser_download_url')" \
  && curl -fsSL -o "/tmp/${asset}" "${download}" \
  && curl -fsSL -o /tmp/SHASUMS256.txt "${sums_url}" \
  && (cd /tmp && awk -v name="${asset}" '$2 == name { print $1 "  " $2 }' \
       SHASUMS256.txt | sha256sum -c --strict) \
  && tar -xzf "/tmp/${asset}" -C /tmp mise/bin/mise \
  && install -m 0755 /tmp/mise/bin/mise /usr/local/bin/mise \
  && rm -rf "/tmp/${asset}" /tmp/SHASUMS256.txt /tmp/mise

ENV MISE_DATA_DIR="/home/dev/.local/share/mise"
ENV MISE_CONFIG_DIR="/home/dev/.local/share/mise/config"
ENV MISE_CACHE_DIR="/home/dev/.local/share/mise/cache"
ENV MISE_STATE_DIR="/home/dev/.local/share/mise/state"
ENV MISE_TRUSTED_CONFIG_PATHS="/workspace"
ENV PATH="/home/dev/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
RUN printf 'export PATH="/home/dev/.local/share/mise/shims:$PATH"\n' \
      > /etc/profile.d/prison-mise.sh

# `C.UTF-8` needs no locale generation. Without it, full-screen interfaces
# draw boxes in ASCII.
ENV LANG=C.UTF-8

# The `dev` ids match the host user so that the bind-mounted project keeps
# its ownership. `prison build` sets `UID`. An existing user or group at that
# id gets the `dev` name.
ARG UID=501
RUN existing_group="$(getent group "${UID}" | cut -d: -f1 || true)" \
  && if [ -n "${existing_group}" ] && [ "${existing_group}" != "dev" ]; then \
       groupmod -n dev "${existing_group}"; \
     elif [ -z "${existing_group}" ]; then \
       groupadd -g "${UID}" dev; \
     fi \
  && existing_user="$(getent passwd "${UID}" | cut -d: -f1 || true)" \
  && if [ -n "${existing_user}" ] && [ "${existing_user}" != "dev" ]; then \
       usermod -l dev -d /home/dev -m -s /bin/bash "${existing_user}"; \
     elif [ -z "${existing_user}" ]; then \
       useradd -m -u "${UID}" -g "${UID}" -s /bin/bash dev; \
     fi \
  && mkdir -p /home/dev /workspace \
  && chown -R "${UID}:${UID}" /home/dev /workspace

# Root owns the agent so that the confined user cannot edit it.
COPY --chown=root:root --chmod=0755 prison-guest /usr/local/bin/prison-guest

WORKDIR /workspace

# The agent runs as root PID 1 so that it can install the network rules. It
# then runs the command as `dev`.
ENTRYPOINT ["/usr/local/bin/prison-guest", "init"]
CMD ["sleep", "infinity"]
