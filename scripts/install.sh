#!/bin/bash

set -e

# Configuration variables
INSTALL_DIR="/opt/domain-dispatcher"
CONFIG_DIR="/etc/domain-dispatcher"
LOG_DIR="/var/log/domain-dispatcher"
SERVICE_USER="domain-dispatcher"
SERVICE_FILE="/etc/systemd/system/domain-dispatcher.service"

echo "=== Domain Dispatcher Installation Script ==="
echo

# Check if running as root
if [ "$EUID" -ne 0 ]; then 
    echo "Error: Please run this script with sudo"
    exit 1
fi

# Check if binary file exists
if [ ! -f "dist/linux-amd64/domain-dispatcher" ]; then
    echo "Error: Binary file not found, please run 'make build-linux' first"
    exit 1
fi

# 1. Create system user
echo "1. Creating system user..."
if ! id "$SERVICE_USER" &>/dev/null; then
    useradd -r -s /bin/false -M "$SERVICE_USER"
    echo "   User $SERVICE_USER created successfully"
else
    echo "   User $SERVICE_USER already exists"
fi

# 2. Create directories
echo "2. Creating installation directories..."
mkdir -p "$INSTALL_DIR"
mkdir -p "$CONFIG_DIR"
mkdir -p "$LOG_DIR"

# 3. Stop existing service (if exists)
if systemctl is-active --quiet domain-dispatcher; then
    echo "3. Stopping existing service..."
    systemctl stop domain-dispatcher
else
    echo "3. No service to stop (not running)"
fi

# 4. Copy files
echo "4. Copying program files..."
cp dist/linux-amd64/domain-dispatcher "$INSTALL_DIR/"
if [ -f "$CONFIG_DIR/config.yaml" ]; then
    echo "   Existing configuration kept: $CONFIG_DIR/config.yaml"
else
    cp config.example.yaml "$CONFIG_DIR/config.yaml"
fi

# 5. Set permissions
echo "5. Setting file permissions..."
chmod +x "$INSTALL_DIR/domain-dispatcher"
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$CONFIG_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR"

# 6. Create systemd service file
echo "6. Creating systemd service..."
cat > "$SERVICE_FILE" << 'EOF'
[Unit]
Description=Domain Dispatcher HTTP Request Router
Documentation=https://github.com/linfan/domain-dispatcher
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=domain-dispatcher
Group=domain-dispatcher
WorkingDirectory=/opt/domain-dispatcher
ExecStart=/opt/domain-dispatcher/domain-dispatcher -c /etc/domain-dispatcher/config.yaml
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536
LimitNPROC=4096
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/domain-dispatcher
StandardOutput=journal
StandardError=journal
SyslogIdentifier=domain-dispatcher

[Install]
WantedBy=multi-user.target
EOF

# 7. Reload systemd
echo "7. Reloading systemd configuration..."
systemctl daemon-reload

# 8. Enable and start service
echo "8. Enabling and starting service..."
systemctl enable domain-dispatcher
systemctl start domain-dispatcher

# 9. Wait for service to start
sleep 2

# 10. Check service status
echo "9. Checking service status..."
if systemctl is-active --quiet domain-dispatcher; then
    echo "   ✓ Service started successfully!"
else
    echo "   ✗ Service failed to start, please check logs:"
    echo "   sudo journalctl -u domain-dispatcher -n 50"
    exit 1
fi

echo
echo "=== Installation Complete ==="
echo
echo "Service management commands:"
echo "  Start service:   sudo systemctl start domain-dispatcher"
echo "  Stop service:    sudo systemctl stop domain-dispatcher"
echo "  Restart service: sudo systemctl restart domain-dispatcher"
echo "  Check status:    sudo systemctl status domain-dispatcher"
echo "  View logs:       sudo journalctl -u domain-dispatcher -f"
echo
echo "Configuration file location: $CONFIG_DIR/config.yaml"
echo "Program installation location: $INSTALL_DIR/"
echo "Log directory: $LOG_DIR/"
echo
