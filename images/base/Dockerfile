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
