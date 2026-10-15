# Domain Dispatcher Deployment Guide

This document describes deploying Domain Dispatcher as a systemd service on
Linux systems.

## Build

```bash
make build-linux    # dist/linux-amd64/domain-dispatcher
```

Other platforms: `make build-linux-arm64`, `build-macos`, `build-windows`,
`build-all` (outputs under `dist/`); `make release` builds and packages
archives. Version and build time are injected via `-ldflags` and printed by
`--version` and at startup.

## Manual Deployment

### 1. Install files

```bash
sudo useradd -r -s /bin/false -M domain-dispatcher
sudo mkdir -p /opt/domain-dispatcher /etc/domain-dispatcher /var/log/domain-dispatcher

sudo cp dist/linux-amd64/domain-dispatcher /opt/domain-dispatcher/
sudo cp config.example.yaml /etc/domain-dispatcher/config.yaml
sudo chmod +x /opt/domain-dispatcher/domain-dispatcher
sudo chown -R domain-dispatcher:domain-dispatcher /opt/domain-dispatcher /etc/domain-dispatcher /var/log/domain-dispatcher
```

### 2. Edit the configuration

The configuration is overlaid on built-in defaults, so
`/etc/domain-dispatcher/config.yaml` only needs the values you want to
change. The `listeners` section is the only required part:

```yaml
listeners:
  - port: 8080
    dispatch:
      - domain: api.example.com
        forward_to: http://localhost:3000

  # TLS certificates are domain-bound: each dispatch rule carries its own
  # certificate and the one matching the client's TLS SNI name is presented.
  # The "*" rule's certificate is the fallback for unknown domains and
  # clients that send no SNI.
  - port: 443
    type: https
    dispatch:
      - domain: api.example.com
        forward_to: http://localhost:3000
        tls:
          cert_file: /etc/domain-dispatcher/certs/api.example.com.crt
          key_file: /etc/domain-dispatcher/certs/api.example.com.key
      - domain: "*"
        forward_to: http://localhost:3000
        tls:
          cert_file: /etc/domain-dispatcher/certs/default.crt
          key_file: /etc/domain-dispatcher/certs/default.key
```

All supported options (timeouts, logging, blacklist, redirect/mock rules,
wildcards) are documented in `config.example.yaml`. Invalid configuration
makes the process exit with code 1 and a precise error message — check
`journalctl` after editing.

### 3. Create the systemd service

Create `/etc/systemd/system/domain-dispatcher.service`:

```ini
[Unit]
Description=Domain Dispatcher HTTP Request Router
Documentation=https://github.com/linfan/domain-dispatcher
After=network-online.target
Wants=network-online.target

[Service]
User=domain-dispatcher
Group=domain-dispatcher
ExecStart=/opt/domain-dispatcher/domain-dispatcher -c /etc/domain-dispatcher/config.yaml
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# Allow binding privileged ports (e.g. 443) as the unprivileged service user
AmbientCapabilities=CAP_NET_BIND_SERVICE

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/domain-dispatcher

[Install]
WantedBy=multi-user.target
```

### 4. Start the service

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now domain-dispatcher
```

## Installation Script

`scripts/install.sh` automates the steps above — it creates the service user
and directories, copies the binary, installs `config.example.yaml` as
`/etc/domain-dispatcher/config.yaml` (kept if it already exists), and
installs the systemd unit shown above:

```bash
make build-linux
sudo ./scripts/install.sh
```

Edit `/etc/domain-dispatcher/config.yaml`, then
`sudo systemctl restart domain-dispatcher`.

## Service Management

```bash
sudo systemctl start|stop|restart|status domain-dispatcher
sudo systemctl enable|disable domain-dispatcher
```

Configuration changes are applied by restarting the service (there is no hot
reload).

## Logs

Logs go to journald by default; file logging with size-based rotation can be
enabled via `global.log` in the configuration.

```bash
sudo journalctl -u domain-dispatcher -f         # follow in real time
sudo journalctl -u domain-dispatcher -n 100     # last 100 lines
```

Every request prints one summary line; set `global.debug: true` for
additional per-rule detail.

## Troubleshooting

```bash
sudo systemctl status domain-dispatcher
sudo journalctl -u domain-dispatcher -n 50 --no-pager
```

Startup failures (missing/invalid config, an `https` listener without a
certificate, missing certificate files, a port already in use) are printed
with a precise message. Run the binary manually to see it directly:

```bash
sudo /opt/domain-dispatcher/domain-dispatcher -c /etc/domain-dispatcher/config.yaml
```

Port conflicts: `sudo ss -tlnp | grep <port>`. Remember to open the listener
ports in the firewall (e.g. `sudo ufw allow 443/tcp`).

Permission problems are usually fixed by re-running the `chown`/`chmod`
commands from step 1.

## Uninstallation

```bash
sudo ./scripts/uninstall.sh
```

Or manually: stop and disable the service, remove
`/etc/systemd/system/domain-dispatcher.service` and run `daemon-reload`,
delete `/opt/domain-dispatcher`, `/etc/domain-dispatcher` and
`/var/log/domain-dispatcher`, then `sudo userdel domain-dispatcher`.
