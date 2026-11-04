# --- gluetun compatible control server ------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS control-server
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=unknown CREATED=unknown
WORKDIR /src
COPY control-server/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.created=${CREATED}" \
      -o /out/control-server .

# --- NordVPN image --------------------------------------------------------------
FROM ghcr.io/linuxserver/baseimage-ubuntu:resolute

LABEL org.opencontainers.image.title="nordvpn" \
      org.opencontainers.image.description="Official NordVPN Linux client in a container with an iptables kill switch" \
      org.opencontainers.image.source="https://github.com/RioNoir/nordvpn" \
      org.opencontainers.image.licenses="AGPL-3.0"

ARG NORDVPN_VERSION=6.0.2
ARG DEBIAN_FRONTEND=noninteractive

# --no-install-recommends keeps the image small: since 5.3.0 the nordvpn
# package recommends the desktop GUI (nordvpn-gui), which is useless here.
RUN apt-get update -y && \
    apt-get install -y --no-install-recommends \
        ca-certificates curl iproute2 iptables iputils-ping wireguard-tools && \
    curl -fsSL https://repo.nordvpn.com/deb/nordvpn/debian/pool/main/n/nordvpn-release/nordvpn-release_1.0.0_all.deb --output /tmp/nordrepo.deb && \
    apt-get install -y --no-install-recommends /tmp/nordrepo.deb && \
    apt-get update -y && \
    apt-get install -y --no-install-recommends nordvpn${NORDVPN_VERSION:+=$NORDVPN_VERSION} && \
    apt-get remove -y nordvpn-release && \
    apt-get autoremove -y && \
    apt-get autoclean -y && \
    rm -rf \
		/tmp/* \
		/var/cache/apt/archives/* \
		/var/lib/apt/lists/* \
		/var/tmp/*

COPY /rootfs /
COPY --from=control-server /out/control-server /usr/bin/control-server
RUN chmod +x /etc/cont-init.d/* /etc/services.d/nordvpn/* /etc/services.d/control-server/run \
    /usr/bin/dockerNetworks /usr/bin/dockerNetworks6 /usr/bin/nord_config /usr/bin/nord_connect /usr/bin/nord_login /usr/bin/nord_watch \
    /usr/bin/nord_start /usr/bin/nord_info \
    /etc/services.d/nordvpn/data/check /usr/bin/healthcheck

HEALTHCHECK --interval=60s --timeout=30s --start-period=120s \
            CMD /usr/bin/healthcheck

EXPOSE 8000/tcp

ENV S6_CMD_WAIT_FOR_SERVICES=1
CMD ["/usr/bin/nord_start"]
