# Domain Dispatcher Deployment Guide

This document describes deploying Domain Dispatcher as a systemd service on
Linux systems.

## Table of Contents

- [Building Binaries](#building-binaries)
- [Linux Service Deployment](#linux-service-deployment)
  - [Method 1: Manual Deployment](#method-1-manual-deployment)
  - [Method 2: Using Installation Script](#method-2-using-installation-script)
- [Service Management](#service-management)
- [Log Viewing](#log-viewing)
- [Troubleshooting](#troubleshooting)
- [Uninstallation](#uninstallation)

---

## Building Binaries

Run the following in the project root:

```bash
# Local build for the current platform (build/domain-dispatcher)
make build

# Tests
make test
```

Cross-platform builds:

```bash
# Build Linux (amd64) version
make build-linux

# Build Linux (arm64) version
make build-linux-arm64

# Build macOS (Intel) version
make build-macos

# Build macOS (Apple Silicon) version
make build-macos-arm64

# Build Windows version
make build-windows

# Build all platform versions at once
make build-all

# Build and package release archives
make release
```

Build output locations:

- Local build: `build/domain-dispatcher`
- Platform binaries: `dist/<platform>-<arch>/domain-dispatcher` (each directory also contains `config.yaml`)
- Release archives: `dist/domain-dispatcher-<version>-<platform>-<arch>.tar.gz` (zip for Windows)

Version and build time are injected via `-ldflags` (`git describe` / UTC
timestamp) and printed by `--version` and at startup.

---

## Linux Service Deployment

### Method 1: Manual Deployment

#### 1. Prepare Binary Files

```bash
# Build Linux version
make build-linux

# Or extract from a release package
tar -xzf domain-dispatcher-v1.0.0-linux-amd64.tar.gz
```

#### 2. Create System User

For security reasons, run the service as a dedicated system user:

```bash
# Create system user (no login permission, no home directory)
sudo useradd -r -s /bin/false -M domain-dispatcher
```

#### 3. Install Files to System Directories

```bash
# Create installation directories
sudo mkdir -p /opt/domain-dispatcher
sudo mkdir -p /etc/domain-dispatcher
sudo mkdir -p /var/log/domain-dispatcher

# Copy binary
sudo cp dist/linux-amd64/domain-dispatcher /opt/domain-dispatcher/

# Install the configuration template and edit it
sudo cp config.example.yaml /etc/domain-dispatcher/config.yaml
sudo nano /etc/domain-dispatcher/config.yaml

# Set permissions
sudo chmod +x /opt/domain-dispatcher/domain-dispatcher
sudo chmod 644 /etc/domain-dispatcher/config.yaml
sudo chown -R domain-dispatcher:domain-dispatcher /opt/domain-dispatcher
sudo chown -R domain-dispatcher:domain-dispatcher /var/log/domain-dispatcher
```

#### 4. Configuration File Adjustment

The configuration is overlaid on built-in defaults, so `/etc/domain-dispatcher/config.yaml`
only needs the values you want to change. A typical production config:

```yaml
global:
  forward:
    request_timeout: 60s
    stream_idle_timeout: 120s
    response_body_timeout: 300s
  server:
    shutdown_timeout: 30s
  log:
    dir: /var/log/domain-dispatcher
    filename: app.log
    max_size: 100
    max_backups: 10
  source_ip_blacklist: []
  add_proxy_headers: true
  debug: false

listeners:
  - port: 8080
    type: http
    dispatch:
      - domain: api.example.com
        forward_to: http://localhost:3000
      - domain: old.example.com
        forward_to: https://new.example.com
        type: redirect
      - domain: health.example.com
        type: mock
        mock_status: 200
        mock_headers:
          Content-Type: application/json
        mock_body: '{"status":"ok"}'

  # TLS certificates are domain-bound, so each dispatch rule reachable over
  # HTTPS carries its own certificate; the one matching the client's TLS SNI
  # name is presented. The "*" rule's certificate is the fallback for unknown
  # domains and clients that send no SNI.
  - port: 8443
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

Configuration errors (unknown timings, an `https` listener without a
certificate on any dispatch rule, missing certificate files, an invalid
blacklist entry, a bad port) make the process exit with
code 1 and a precise error message — check `journalctl` after editing.

#### 5. Create systemd Service File

Create `/etc/systemd/system/domain-dispatcher.service`:

```ini
[Unit]
Description=Domain Dispatcher HTTP Request Router
Documentation=https://github.com/freyrlin/domain-dispatcher
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=domain-dispatcher
Group=domain-dispatcher

# Working directory
WorkingDirectory=/opt/domain-dispatcher

# Binary and config path
ExecStart=/opt/domain-dispatcher/domain-dispatcher -c /etc/domain-dispatcher/config.yaml

# Restart policy
Restart=on-failure
RestartSec=5s

# Resource limits
LimitNOFILE=65536
LimitNPROC=4096

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/domain-dispatcher

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=domain-dispatcher

[Install]
WantedBy=multi-user.target
```

#### 6. Enable and Start Service

```bash
sudo systemctl daemon-reload
sudo systemctl enable domain-dispatcher
sudo systemctl start domain-dispatcher
sudo systemctl status domain-dispatcher
```

---

### Method 2: Using Installation Script

The project provides `scripts/install.sh`, which creates the service user and
directories, copies the binary, installs `config.example.yaml` as
`/etc/domain-dispatcher/config.yaml` (kept if it already exists), and installs
the systemd unit shown above:

```bash
# Build the Linux binary first
make build-linux

# Run the installer
sudo ./scripts/install.sh
```

Afterwards edit `/etc/domain-dispatcher/config.yaml` and restart:

```bash
sudo systemctl restart domain-dispatcher
```

---

## Service Management

```bash
# Start / stop / restart
sudo systemctl start domain-dispatcher
sudo systemctl stop domain-dispatcher
sudo systemctl restart domain-dispatcher

# Status and boot behaviour
sudo systemctl status domain-dispatcher
sudo systemctl enable domain-dispatcher
sudo systemctl disable domain-dispatcher
```

Configuration changes are applied by restarting the service (there is no hot
reload):

```bash
sudo vim /etc/domain-dispatcher/config.yaml
sudo systemctl restart domain-dispatcher
```

---

## Log Viewing

The service logs to journald by default. Log rotation is handled by journald;
alternatively enable file logging via `global.log` (with `dir` and `filename`),
which uses lumberjack size-based rotation.

```bash
# Follow logs in real time
sudo journalctl -u domain-dispatcher -f

# Last 100 lines
sudo journalctl -u domain-dispatcher -n 100

# Since a point in time
sudo journalctl -u domain-dispatcher --since "1 hour ago"
```

Every request prints one summary line:

```
[METHOD] source=<ip:port> target=<url> req_size=<n> resp_status=<code> resp_size=<n> duration=<d>
```

Set `global.debug: true` for additional per-rule detail lines.

---

## Troubleshooting

### Service fails to start

```bash
sudo systemctl status domain-dispatcher
sudo journalctl -u domain-dispatcher -n 50 --no-pager
```

The startup log points at the exact problem: missing/invalid config file,
`no listeners configured`, an invalid port or dispatch rule type, an `https`
listener whose rules configure no TLS certificate or missing certificate
files, an invalid blacklist entry, or a port
already in use. Run the binary manually to see the error directly:

```bash
/opt/domain-dispatcher/domain-dispatcher -c /etc/domain-dispatcher/config.yaml
```

### Permission denied errors

```bash
sudo chown -R domain-dispatcher:domain-dispatcher /opt/domain-dispatcher
sudo chown domain-dispatcher:domain-dispatcher /var/log/domain-dispatcher
sudo chmod 755 /opt/domain-dispatcher
sudo chmod 644 /etc/domain-dispatcher/config.yaml
```

### Port already in use

```bash
sudo ss -tlnp | grep 8080
sudo lsof -i :8080
```

Change the listener `port` in the configuration or stop the conflicting process.

### Firewall

Open the configured listener ports:

```bash
# ufw (Ubuntu/Debian)
sudo ufw allow 8080/tcp

# firewalld (CentOS/RHEL)
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload

# iptables
sudo iptables -A INPUT -p tcp --dport 8080 -j ACCEPT
```

---

## Uninstallation

Using the script (prompts before removing config, logs and the system user):

```bash
sudo ./scripts/uninstall.sh
```

Manual removal:

```bash
# Stop and disable the service
sudo systemctl stop domain-dispatcher
sudo systemctl disable domain-dispatcher

# Remove the service file and reload systemd
sudo rm /etc/systemd/system/domain-dispatcher.service
sudo systemctl daemon-reload

# Remove files
sudo rm -rf /opt/domain-dispatcher
sudo rm -rf /etc/domain-dispatcher
sudo rm -rf /var/log/domain-dispatcher

# Remove the system user
sudo userdel domain-dispatcher
```

## File Locations Summary

| File/Directory | Purpose |
|----------------|---------|
| `/opt/domain-dispatcher/domain-dispatcher` | Binary executable |
| `/etc/domain-dispatcher/config.yaml` | Configuration file |
| `/var/log/domain-dispatcher/` | Log directory (when file logging is enabled) |
| `/etc/systemd/system/domain-dispatcher.service` | Systemd service file |

## References

- [systemd Service Management Documentation](https://www.freedesktop.org/software/systemd/man/systemd.service.html)
- [journalctl Log Management](https://www.freedesktop.org/software/systemd/man/journalctl.html)
