ARG PRISON_BASE
FROM ${PRISON_BASE}

ARG OPENCODE_VERSION=latest
ARG OPENCODE_RELEASES=https://api.github.com/repos/anomalyco/opencode/releases

RUN set -eu \
  && case "$(dpkg --print-architecture)" in \
       amd64) architecture=x64 ;; \
       arm64) architecture=arm64 ;; \
       *) echo "unsupported architecture" >&2; exit 1 ;; \
     esac \
  && if [ "${OPENCODE_VERSION}" = latest ]; then \
       release_path=latest; \
     else \
       release_path="tags/v${OPENCODE_VERSION#v}"; \
     fi \
  && release="$(curl -fsSL "${OPENCODE_RELEASES}/${release_path}")" \
  && asset="opencode-linux-${architecture}.tar.gz" \
  && download="$(printf '%s' "${release}" | jq -er --arg name "${asset}" \
       '.assets[] | select(.name == $name) | .browser_download_url')" \
  && checksum="$(printf '%s' "${release}" | jq -er --arg name "${asset}" \
       '.assets[] | select(.name == $name) | .digest | ltrimstr("sha256:")')" \
  && curl -fsSL -o /tmp/opencode.tar.gz "${download}" \
  && printf '%s  /tmp/opencode.tar.gz\n' "${checksum}" | sha256sum -c - \
  && tar -xzf /tmp/opencode.tar.gz -C /tmp opencode \
  && install -m 0755 /tmp/opencode /usr/local/bin/opencode \
  && rm -f /tmp/opencode.tar.gz /tmp/opencode

ARG MODELS_URL=https://models.dev/api.json
RUN curl -fsSL -o /tmp/models.json "${MODELS_URL}" \
  && jq -e ' \
      def broker: sub("^https://opencode\\.ai/"; \
        "http://opencode.inmate.prison.internal/"); \
      (.opencode, .["opencode-go"]) |= ( \
        .api |= broker \
        | .models[] |= (if .provider.api then .provider.api |= broker \
                        else . end))' \
      /tmp/models.json > /usr/local/share/opencode-models.json \
  && rm -f /tmp/models.json
