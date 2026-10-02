ARG PRISON_BASE
FROM ${PRISON_BASE}

ARG AGY_RELEASES=antigravity-cli-auto-updater-974169037036.us-central1.run.app

RUN set -eu \
  && architecture="$(dpkg --print-architecture)" \
  && manifest="$(curl -fsSL \
       "https://${AGY_RELEASES}/manifests/linux_${architecture}.json")" \
  && download="$(printf '%s' "${manifest}" | jq -er .url)" \
  && checksum="$(printf '%s' "${manifest}" | jq -er .sha512)" \
  && curl -fsSL -o /tmp/agy.tar.gz "${download}" \
  && printf '%s  /tmp/agy.tar.gz\n' "${checksum}" | sha512sum -c - \
  && tar -xzf /tmp/agy.tar.gz -C /tmp antigravity \
  && install -m 0755 /tmp/antigravity /usr/local/bin/agy \
  && rm -f /tmp/agy.tar.gz /tmp/antigravity
