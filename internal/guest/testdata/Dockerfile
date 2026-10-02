# Minimal image for the guest integration test.

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      iproute2 \
      iptables \
  && rm -rf /var/lib/apt/lists/*

RUN groupadd -g 501 dev && useradd -m -u 501 -g 501 -s /bin/bash dev

COPY --chown=root:root --chmod=0755 prison-guest /usr/local/bin/prison-guest

ENTRYPOINT ["/usr/local/bin/prison-guest", "init"]
CMD ["sleep", "infinity"]
