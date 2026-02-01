# Deployment Guide

This guide explains how to deploy the Domain Dispatcher to a Linux environment and configure it as a systemd service.

## Overview

Domain Dispatcher is a lightweight Go application that routes HTTP requests based on the Host header to different backend services. It supports both forwarding (proxy) and redirect rules.

## Prerequisites

- Linux server (Ubuntu 20.04+, CentOS 7+, Debian 10+, or similar)
- Root or sudo access
- Go 1.21+ (only needed if building from source)

## Deployment Steps

### 1. Build the Binary

Build the Linux binary on your development machine or on the target server:

```bash
# Clone or navigate to the project directory
cd domain-dispatcher

# Build for Linux AMD64
make build-linux

# Or build manually
go build -o dist/domain-dispatcher-linux-amd64
```

The binary will be created at `dist/domain-dispatcher-linux-amd64`.

### 2. Prepare the Server

On your Linux server, create the necessary directories and user:

```bash
# Create system user (no login, no home directory)
sudo useradd -r -s /bin/false domain-dispatcher

# Create directories
sudo mkdir -p /opt/domain-dispatcher
sudo mkdir -p /etc/domain-dispatcher
sudo mkdir -p /var/log/domain-dispatcher

# Set ownership
sudo chown domain-dispatcher:domain-dispatcher /opt/domain-dispatcher
sudo chown domain-dispatcher:domain-dispatcher /var/log/domain-dispatcher
```

### 3. Deploy Files

Copy the binary and configuration to the server:

```bash
# Copy binary
sudo cp dist/domain-dispatcher-linux-amd64 /opt/domain-dispatcher/domain-dispatcher
sudo chmod +x /opt/domain-dispatcher/domain-dispatcher

# Copy and edit configuration
sudo cp config.yaml.sample /etc/domain-dispatcher/config.yaml
sudo chmod 644 /etc/domain-dispatcher/config.yaml

# Set ownership
sudo chown -R domain-dispatcher:domain-dispatcher /opt/domain-dispatcher
sudo chown root:root /etc/domain-dispatcher/config.yaml
```

### 4. Configure the Application

Edit the configuration file at `/etc/domain-dispatcher/config.yaml`:

```bash
sudo nano /etc/domain-dispatcher/config.yaml
```

Example configuration:

```yaml
- port: 8080
  dispatch:
    - domain: api.example.com
      forward_to: http://localhost:3000
      type: forward
    - domain: old.example.com
      forward_to: https://new.example.com
      type: redirect
- port: 8081
  dispatch:
    - domain: admin.example.com
      forward_to: http://localhost:3001
      type: forward
```

**Configuration fields:**
- `port`: The port to listen on
- `domain`: The domain to match (from Host header)
- `forward_to`: Target URL
- `type`: Either `forward` (proxy) or `redirect` (HTTP 302)

### 5. Create Systemd Service

Create the systemd service file:

```bash
sudo nano /etc/systemd/system/domain-dispatcher.service
```

Add the following content:

```ini
[Unit]
Description=Domain Dispatcher - HTTP Request Router
Documentation=https://github.com/yourusername/domain-dispatcher
After=network.target
Wants=network.target

[Service]
Type=simple
User=domain-dispatcher
Group=domain-dispatcher

# Working directory
WorkingDirectory=/opt/domain-dispatcher

# Binary and config path
ExecStart=/opt/domain-dispatcher/domain-dispatcher /etc/domain-dispatcher/config.yaml

# Restart policy
Restart=on-failure
RestartSec=5

# Resource limits
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/domain-dispatcher
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=domain-dispatcher

[Install]
WantedBy=multi-user.target
```

### 6. Start the Service

Reload systemd and start the service:

```bash
# Reload systemd daemon
sudo systemctl daemon-reload

# Enable service to start on boot
sudo systemctl enable domain-dispatcher

# Start the service
sudo systemctl start domain-dispatcher

# Check status
sudo systemctl status domain-dispatcher
```

### 7. Verify Deployment

Check that the service is running:

```bash
# View logs
sudo journalctl -u domain-dispatcher -f

# Test the endpoint
curl -H "Host: api.example.com" http://localhost:8080/
```

## Managing the Service

### Start/Stop/Restart

```bash
# Start
sudo systemctl start domain-dispatcher

# Stop
sudo systemctl stop domain-dispatcher

# Restart
sudo systemctl restart domain-dispatcher

# Reload configuration (requires restart)
sudo systemctl restart domain-dispatcher
```

### View Logs

```bash
# View all logs
sudo journalctl -u domain-dispatcher

# Follow logs in real-time
sudo journalctl -u domain-dispatcher -f

# View logs since last boot
sudo journalctl -u domain-dispatcher --since today

# View logs with specific time range
sudo journalctl -u domain-dispatcher --since "1 hour ago"
```

### Update Configuration

After modifying `/etc/domain-dispatcher/config.yaml`:

```bash
# Validate configuration syntax
sudo /opt/domain-dispatcher/domain-dispatcher /etc/domain-dispatcher/config.yaml --dry-run

# Restart service to apply changes
sudo systemctl restart domain-dispatcher

# Verify new configuration is active
sudo systemctl status domain-dispatcher
```

## Firewall Configuration

If using a firewall, open the required ports:

```bash
# Using ufw (Ubuntu/Debian)
sudo ufw allow 8080/tcp
sudo ufw allow 8081/tcp

# Using firewalld (CentOS/RHEL)
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --permanent --add-port=8081/tcp
sudo firewall-cmd --reload

# Using iptables
sudo iptables -A INPUT -p tcp --dport 8080 -j ACCEPT
sudo iptables -A INPUT -p tcp --dport 8081 -j ACCEPT
```

## Reverse Proxy Setup (Optional)

For production use, place Domain Dispatcher behind a reverse proxy like Nginx:

```nginx
server {
    listen 80;
    server_name api.example.com;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Troubleshooting

### Service fails to start

```bash
# Check detailed error message
sudo journalctl -u domain-dispatcher -n 50

# Check configuration syntax
sudo /opt/domain-dispatcher/domain-dispatcher /etc/domain-dispatcher/config.yaml

# Verify file permissions
ls -la /opt/domain-dispatcher/
ls -la /etc/domain-dispatcher/
```

### Permission denied errors

```bash
# Fix ownership
sudo chown -R domain-dispatcher:domain-dispatcher /opt/domain-dispatcher
sudo chown domain-dispatcher:domain-dispatcher /etc/domain-dispatcher/config.yaml
sudo chown domain-dispatcher:domain-dispatcher /var/log/domain-dispatcher

# Fix permissions
sudo chmod 755 /opt/domain-dispatcher
sudo chmod 644 /etc/domain-dispatcher/config.yaml
```

### Port already in use

```bash
# Find process using the port
sudo lsof -i :8080

# Kill the process or change the port in config.yaml
```

## Uninstallation

```bash
# Stop and disable service
sudo systemctl stop domain-dispatcher
sudo systemctl disable domain-dispatcher

# Remove files
sudo rm -rf /opt/domain-dispatcher
sudo rm -rf /etc/domain-dispatcher
sudo rm -rf /var/log/domain-dispatcher
sudo rm /etc/systemd/system/domain-dispatcher.service

# Remove user
sudo userdel domain-dispatcher

# Reload systemd
sudo systemctl daemon-reload
```

## File Locations Summary

| File/Directory | Purpose |
|----------------|---------|
| `/opt/domain-dispatcher/domain-dispatcher` | Binary executable |
| `/etc/domain-dispatcher/config.yaml` | Configuration file |
| `/var/log/domain-dispatcher/` | Log directory (if file logging enabled) |
| `/etc/systemd/system/domain-dispatcher.service` | Systemd service file |
