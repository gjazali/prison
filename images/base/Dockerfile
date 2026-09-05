# The base image every prison box is built on.
#
# Carries the guest agent, the unprivileged user, and terminal
# descriptions. Inmate images layer on top through `ARG PRISON_BASE`.
# The foundation defaults to a bare Debian.

ARG PRISON_FOUNDATION=debian:bookworm-slim
FROM ${PRISON_FOUNDATION}

# `ncurses-term` carries the terminal descriptions the base system leaves
# out. `iptables` and `iproute2` are what the guest agent builds the
# confinement out of.
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

# `sudo` is installed in every box but grants nothing by default. The
# guest agent writes a rule into `/etc/sudoers.d` only when the box was
# created with sudo enabled.

# Shims for pnpm and yarn, so `prison up` can install a project using
# either. A foundation without Node has no corepack and skips this.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN if command -v corepack >/dev/null 2>&1; then corepack enable; fi

# `C.UTF-8` needs no locale generation. Without it, full-screen interfaces
# fall back to ASCII for box-drawing characters.
ENV LANG=C.UTF-8

# The user and group ids match the host user, so the bind-mounted project
# directory keeps matching ownership. `prison build` supplies the value as
# `--build-arg UID=$(id -u)`. An existing user or group at that id is
# renamed rather than replaced.
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

# `make build` cross-compiles the agent into this context. It is owned by
# root and unwritable by `dev`, so the confined user cannot edit what
# confines it.
COPY --chown=root:root --chmod=0755 prison-guest /usr/local/bin/prison-guest

WORKDIR /workspace

# The box stays root as PID 1 so the agent can install the network rules,
# then it spawns the command as `dev`.
ENTRYPOINT ["/usr/local/bin/prison-guest", "init"]
CMD ["sleep", "infinity"]
