# testing target: same Linux binary the box runs, on a Linux kernel
# Docker Hub is blocked from here; the mirror is only a pull path.
FROM docker.m.daocloud.io/library/debian:bookworm-slim

ARG SINGBOX_VERSION=1.14.4
ARG TARGETARCH=arm64

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl iproute2 nftables conntrack dnsmasq tzdata iputils-ping procps \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="${TARGETARCH}"; \
    curl -fsSL -o /tmp/sb.tgz \
      "https://github.com/SagerNet/sing-box/releases/download/v${SINGBOX_VERSION}/sing-box-${SINGBOX_VERSION}-linux-${arch}.tar.gz"; \
    tar -xzf /tmp/sb.tgz -C /tmp; \
    install "/tmp/sing-box-${SINGBOX_VERSION}-linux-${arch}/sing-box" /usr/local/bin/sing-box; \
    rm -rf /tmp/sb.tgz "/tmp/sing-box-${SINGBOX_VERSION}-linux-${arch}"

COPY bin/useg-linux /usr/local/bin/useg
COPY dev/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY dev/dnsmasq.conf /etc/dnsmasq.conf
COPY dev/config.yaml /etc/useg/config.yaml
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
