<p align="center">
    <a href="https://nordvpn.com/"><img src="https://github.com/RioNoir/nordvpn/raw/master/.img/NordVpn_logo.png"/></a>
    <br/>
    <a href="https://github.com/RioNoir/nordvpn/blob/master/LICENSE"><img src="https://badgen.net/github/license/RioNoir/nordvpn?color=cyan"/></a>
    <a href="https://github.com/RioNoir/nordvpn/actions/workflows/deploy.yml"><img src="https://github.com/RioNoir/nordvpn/actions/workflows/deploy.yml/badge.svg?branch=master"/></a>
    <a href="https://github.com/RioNoir/nordvpn/pkgs/container/nordvpn"><img src="https://badgen.net/badge/icon/ghcr.io?icon=docker&label=image"/></a>
    <a href="https://github.com/RioNoir/nordvpn"><img src="https://badgen.net/github/stars/RioNoir/nordvpn?icon=github&label=stars&color=black"/></a>
</p>

# NordVPN in Docker

The official [NordVPN Linux client](https://github.com/NordSecurity/nordvpn-linux) packaged in a container.
Start it first, then route any other container through it: all traffic leaves via NordVPN, and an
iptables **kill switch** blocks everything else, even before the tunnel is up.

| Component      | Version                                    |
| -------------- | ------------------------------------------ |
| NordVPN client | **6.0.2**                                  |
| Base image     | `linuxserver/baseimage-ubuntu:resolute` (Ubuntu 26.04 LTS, s6-overlay v3) |
| Architectures  | `linux/amd64`, `linux/arm64`               |

> This is a maintained fork of [bubuntux/nordvpn](https://github.com/bubuntux/nordvpn), which is no longer maintained.

## Contents

- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Docker Compose](#docker-compose)
- [Environment variables](#environment-variables)
- [Upgrading from the 4.x image](#upgrading-from-the-4x-image)
- [Useful commands](#useful-commands)
- [Troubleshooting](#troubleshooting)
- [Building the image](#building-the-image)

## Requirements

- A NordVPN subscription and an **access token**. Create one in [Nord Account](https://my.nordaccount.com/) →
  *NordVPN* → *Manual setup* → *Generate new token*.
- The `NET_ADMIN` and `NET_RAW` capabilities. The container builds its own firewall and network
  interfaces. Without them it refuses to route any traffic.
- A Linux host kernel with WireGuard (NordLynx) support. Any kernel from 5.6 onwards has it built in.

## Quick start

```sh
docker run -d --name vpn \
  --cap-add=NET_ADMIN --cap-add=NET_RAW \
  -e TOKEN=f6f2bb45... \
  -e CONNECT=Italy \
  ghcr.io/rionoir/nordvpn
```

Attach other containers to its network stack:

```sh
docker run -d --net=container:vpn other/image
```

Check that it works:

```sh
docker exec vpn nordvpn status
docker run --rm --net=container:vpn curlimages/curl -s https://api.ipify.org
```

## Docker Compose

### Basic: a service behind the VPN, reachable from the LAN

Every port published by services behind the VPN has to be declared on the `vpn` service.

```yaml
services:
  vpn:
    image: ghcr.io/rionoir/nordvpn
    container_name: vpn
    cap_add:
      - NET_ADMIN
      - NET_RAW
    environment:
      - TOKEN=f6f2bb45...            # required (or TOKENFILE)
      - CONNECT=Italy
      - PROTOCOL=nordlynx
      - NET_LOCAL=192.168.1.0/24     # let your LAN reach the services
    ports:
      - 8080:8080                    # qBittorrent web UI
    sysctls:
      - net.ipv6.conf.all.disable_ipv6=1   # recommended for IPv4-only setups
    restart: unless-stopped

  torrent:
    image: ghcr.io/linuxserver/qbittorrent
    network_mode: service:vpn
    depends_on:
      vpn:
        condition: service_healthy
    restart: unless-stopped
```

The web UI is then available at `http://localhost:8080` or `http://<host-ip>:8080` from the LAN.

### Token from a Docker secret

```yaml
services:
  vpn:
    image: ghcr.io/rionoir/nordvpn
    cap_add: [NET_ADMIN, NET_RAW]
    environment:
      - TOKENFILE=/run/secrets/nordvpn_token
    secrets:
      - nordvpn_token

secrets:
  nordvpn_token:
    file: ./nordvpn_token.txt
```

### Behind a Traefik reverse proxy with HTTPS

```yaml
services:
  traefik:
    image: traefik:v3
    command:
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --entrypoints.web.address=:80
      - --entrypoints.web.http.redirections.entrypoint.to=websecure
      - --entrypoints.websecure.address=:443
      - --entrypoints.websecure.http.tls.certresolver=letsencrypt
      - --certificatesresolvers.letsencrypt.acme.tlschallenge=true
      - --certificatesresolvers.letsencrypt.acme.email=you@example.com
      - --certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json
    ports:
      - 80:80
      - 443:443
    volumes:
      - ./letsencrypt:/letsencrypt
      - /var/run/docker.sock:/var/run/docker.sock:ro
    restart: unless-stopped

  vpn:
    image: ghcr.io/rionoir/nordvpn
    cap_add: [NET_ADMIN, NET_RAW]
    environment:
      - TOKEN=f6f2bb45...
      - CONNECT=Italy
    sysctls:
      - net.ipv6.conf.all.disable_ipv6=1
    restart: unless-stopped

  torrent:
    image: ghcr.io/linuxserver/qbittorrent
    network_mode: service:vpn
    depends_on:
      vpn:
        condition: service_healthy
    labels:
      - traefik.enable=true
      - traefik.http.routers.torrent.rule=Host(`torrent.example.com`)
      - traefik.http.services.torrent.loadbalancer.server.port=8080
    restart: unless-stopped
```

Traefik reaches the services through the Docker network, which the container automatically adds
to the NordVPN allowlist.

## Environment variables

### Connection

| Variable | Default | Description |
| --- | --- | --- |
| `TOKEN` | – | **Required** (unless `TOKENFILE` is set). NordVPN access token. |
| `TOKENFILE` | – | File containing the token, for example `/run/secrets/<name>` with Docker secrets. The first line is read. |
| `CONNECT` | recommended server | Where to connect: a country (`Italy`), a country code (`it`), a city (`Italy Milan`), a server (`it123`) or a group (`Double_VPN`). Flags are passed through, for example `--group double_vpn us`. Run `nordvpn countries`, `nordvpn cities <country>` and `nordvpn groups` to see the valid values. |
| `PROTOCOL` | `nordlynx` | `nordlynx` (WireGuard, the fastest), `openvpn_udp`, `openvpn_tcp`, or `nordwhisper` (obfuscated, for networks that block VPNs). |
| `POST_QUANTUM` | – | `on`/`off`. Post-quantum encryption (NordLynx only). |
| `PRE_CONNECT` | – | Shell command run before connecting. |
| `POST_CONNECT` | – | Shell command run after a successful connection. |

### Features

| Variable | Default | Description |
| --- | --- | --- |
| `PROTECTION` | – | `on`/`off`. Real-time protection, previously Threat Protection Lite / CyberSec. Blocks malicious sites and ads. Works only with NordVPN's own DNS servers. |
| `DNS` | – | Up to 3 custom DNS servers, comma separated (`1.1.1.1,9.9.9.9`), or `off`. Custom DNS turns off real-time protection. |
| `FIREWALL` | – | `on`/`off`. NordVPN's own firewall. The container kill switch stays active regardless. |
| `LAN_DISCOVERY` | – | `on`/`off`. Allows access to private LAN ranges while connected. |
| `MESHNET` | – | `on`/`off`. Enables [Meshnet](https://meshnet.nordvpn.com/). |
| `ALLOWLOCAL` | – | Comma-separated Meshnet peers allowed to access this device's local network, for example `peer-one.nord,peer-two.nord`. |
| `ALLOWROUTE` | – | Comma-separated Meshnet peers allowed to route their traffic through this device. |

### Networking and kill switch

| Variable | Default | Description |
| --- | --- | --- |
| `NET_LOCAL` | – | IPv4 CIDRs (comma or semicolon separated) allowed to reach the container and to receive replies, for example `192.168.1.0/24`. |
| `NET6_LOCAL` | – | Same as `NET_LOCAL`, for IPv6 (`fd00:d34d:b33f::/64`). |
| `ALLOW_LIST` | – | Domains reachable **outside** the VPN (`example.com,example.org`). They are resolved once, at startup. |
| `PORTS` | – | Ports to allowlist for TCP and UDP, separated by `;` or `,` (`9091;9095`). |
| `PORT_RANGE` | – | Port range to allowlist for TCP and UDP (`9091 9095`). |

The Docker networks attached to the container are always allowed. Everything else can only leave
through the tunnel.

### Monitoring

| Variable | Default | Description |
| --- | --- | --- |
| `CHECK_CONNECTION_INTERVAL` | `300` | Seconds between connectivity checks. If a check fails, the daemon is restarted and the connection re-established. |
| `CHECK_CONNECTION_URL` | `www.google.com` | URL used for the connectivity check. |

A Docker `HEALTHCHECK` is built in. It verifies that the client reports `Connected` and that
`https://api.ipify.org` is reachable. Use `depends_on: condition: service_healthy` to start dependent
services only once the tunnel is up.

## Upgrading from the 4.x image

NordVPN 5.x and 6.x removed several CLI commands. The container maps the old variables to the new
ones, so existing setups keep working, but you should migrate:

| Old | New | Notes |
| --- | --- | --- |
| `TECHNOLOGY=NordLynx` | `PROTOCOL=nordlynx` | `set technology` no longer exists. |
| `TECHNOLOGY=OpenVPN` + `PROTOCOL=UDP\|TCP` | `PROTOCOL=openvpn_udp` / `openvpn_tcp` | |
| `OBFUSCATE=on` | `PROTOCOL=nordwhisper` | Obfuscated servers now use NordWhisper. |
| `CYBER_SEC` | `PROTECTION` | Renamed to *Real-time protection*. |
| `WHITELIST` | `ALLOW_LIST` | |
| `NETWORK` / `NETWORK6` | `NET_LOCAL` / `NET6_LOCAL` | |
| `CONNECT=P2P` | `CONNECT=<country>` | The P2P group is gone: all regular servers now support P2P. |
| `CONNECT=Europe`, etc. | `CONNECT=<country>` | Regional groups were removed in 5.0. |

Other changes in this release:

- The `arm/v7` (32-bit) image is no longer built, because the Ubuntu base image does not support it.
- The startup banner that printed the NordLynx private key to the logs has been removed.
- NordVPN's own firewall now uses nftables. The container kill switch still uses iptables (legacy backend).

## Useful commands

```sh
docker exec vpn nordvpn status          # connection status
docker exec vpn nordvpn settings        # current settings
docker exec vpn nordvpn countries       # available countries
docker exec vpn nordvpn groups          # available server groups
docker exec vpn nordvpn connect Germany # switch server on the fly
docker logs -f vpn                      # container logs
```

## Troubleshooting

- **`FATAL: iptables is not functional`**: the container is missing `NET_ADMIN`/`NET_RAW`. Add both capabilities.
- **`Invalid token.`**: the token has expired or was revoked. Generate a new one in Nord Account.
- **Services can't be reached from the LAN**: set `NET_LOCAL` to your LAN subnet and publish the ports on the `vpn` service.
- **Slow or blocked connection**: try `PROTOCOL=nordwhisper` (restrictive networks) or `PROTOCOL=openvpn_tcp`.
- **IPv6 leaks / errors**: if you don't use IPv6, disable it with the `net.ipv6.conf.all.disable_ipv6=1` sysctl.
- For more detail, run `docker exec vpn nordvpn diagnostics`, which collects the logs and network information into a single file.

## Building the image

```sh
docker build -t nordvpn .
# a specific client version
docker build --build-arg NORDVPN_VERSION=6.0.2 -t nordvpn:6.0.2 .
# whatever version is newest in the NordVPN repository
docker build --build-arg NORDVPN_VERSION= -t nordvpn:latest .
```

Every push to GitHub builds the images and publishes them to `ghcr.io/rionoir/nordvpn` and Docker
Hub (`rionoir/nordvpn`), tagged with the branch name. The default branch is also tagged `latest`.

## Disclaimer

This project is independently developed and has no affiliation with NordVPN or Nord Security.
Nord Security is not responsible for, and has no control over, the nature, content and availability
of this project. Licensed under the [AGPL-3.0](LICENSE).
