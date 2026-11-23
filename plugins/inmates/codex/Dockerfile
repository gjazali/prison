ARG PRISON_BASE
FROM ${PRISON_BASE}

ARG CODEX_VERSION=latest
ARG CODEX_RELEASES=https://api.github.com/repos/openai/codex/releases

RUN set -eu \
  && case "$(dpkg --print-architecture)" in \
       amd64) target=x86_64-unknown-linux-musl ;; \
       arm64) target=aarch64-unknown-linux-musl ;; \
       *) echo "unsupported architecture" >&2; exit 1 ;; \
     esac \
  && if [ "${CODEX_VERSION}" = latest ]; then \
       release_path=latest; \
     else \
       version="${CODEX_VERSION#rust-}"; \
       release_path="tags/rust-v${version#v}"; \
     fi \
  && release="$(curl -fsSL "${CODEX_RELEASES}/${release_path}")" \
  && binary="codex-${target}" \
  && asset="${binary}.tar.gz" \
  && download="$(printf '%s' "${release}" | jq -er --arg name "${asset}" \
       '.assets[] | select(.name == $name) | .browser_download_url')" \
  && checksum="$(printf '%s' "${release}" | jq -er --arg name "${asset}" \
       '.assets[] | select(.name == $name) | .digest | ltrimstr("sha256:")')" \
  && curl -fsSL -o "/tmp/${asset}" "${download}" \
  && printf '%s  /tmp/%s\n' "${checksum}" "${asset}" | sha256sum -c - \
  && tar -xzf "/tmp/${asset}" -C /tmp "${binary}" \
  && install -m 0755 "/tmp/${binary}" /usr/local/bin/codex \
  && rm -f "/tmp/${asset}" "/tmp/${binary}"

# System defaults for confined boxes: skip startup update checks (the box cannot
# reach GitHub release endpoints), store credentials as files in CODEX_HOME, and
# run in-process without auto-starting a background app-server daemon.
RUN mkdir -p /etc/codex \
  && printf '%s\n' \
       'check_for_update_on_startup = false' \
       'cli_auth_credentials_store = "file"' \
       '' \
       '[features]' \
       'daemon_auto_start = false' \
       > /etc/codex/config.toml
