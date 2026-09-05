# Throwaway image for the guest integration test. The real base image
# is images/base/Dockerfile; this one carries only what the agent needs
# to confine a box and what the test needs to drive it from inside.

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
