#!/bin/bash
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "================================================"
echo "  Qemubox Uninstallation Script"
echo "================================================"
echo ""

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}Error: This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

# Confirmation prompt
echo -e "${YELLOW}WARNING: This will remove all Qemubox components and data.${NC}"
echo "This includes:"
echo "  - All binaries in /usr/share/qemubox"
echo "  - All configuration files in /usr/share/qemubox/config"
echo "  - All state data in /var/lib/qemubox (containers, images, etc.)"
echo "  - Systemd service files"
echo ""
read -p "Are you sure you want to continue? (yes/no): " -r
echo ""
if [[ ! $REPLY =~ ^[Yy][Ee][Ss]$ ]]; then
    echo "Uninstallation cancelled."
    exit 0
fi

echo "🛑 Stopping services..."

# Stop and disable services
if systemctl is-active --quiet qemubox-containerd; then
    echo "  → Stopping qemubox-containerd service..."
    systemctl stop qemubox-containerd
    echo -e "    ${GREEN}✓${NC} qemubox-containerd stopped"
fi

if systemctl is-enabled --quiet qemubox-containerd 2>/dev/null; then
    echo "  → Disabling qemubox-containerd service..."
    systemctl disable qemubox-containerd
    echo -e "    ${GREEN}✓${NC} qemubox-containerd disabled"
fi

if systemctl is-active --quiet qemubox-buildkit; then
    echo "  → Stopping qemubox-buildkit service..."
    systemctl stop qemubox-buildkit
    echo -e "    ${GREEN}✓${NC} qemubox-buildkit stopped"
fi

if systemctl is-enabled --quiet qemubox-buildkit 2>/dev/null; then
    echo "  → Disabling qemubox-buildkit service..."
    systemctl disable qemubox-buildkit
    echo -e "    ${GREEN}✓${NC} qemubox-buildkit disabled"
fi

echo ""
echo "🗑️  Removing files..."

# Remove systemd service symlinks
if [ -L /etc/systemd/system/qemubox-containerd.service ]; then
    echo "  → Removing qemubox-containerd.service symlink..."
    rm -f /etc/systemd/system/qemubox-containerd.service
    echo -e "    ${GREEN}✓${NC} qemubox-containerd.service symlink removed"
fi

if [ -L /etc/systemd/system/qemubox-buildkit.service ]; then
    echo "  → Removing qemubox-buildkit.service symlink..."
    rm -f /etc/systemd/system/qemubox-buildkit.service
    echo -e "    ${GREEN}✓${NC} qemubox-buildkit.service symlink removed"
fi

systemctl daemon-reload
echo -e "  ${GREEN}✓${NC} Systemd reloaded"

# Remove binaries and configuration
if [ -d /usr/share/qemubox ]; then
    echo "  → Removing /usr/share/qemubox..."
    rm -rf /usr/share/qemubox
    echo -e "    ${GREEN}✓${NC} /usr/share/qemubox removed"
fi

# Remove state data
if [ -d /var/lib/qemubox ]; then
    echo "  → Removing /var/lib/qemubox..."
    rm -rf /var/lib/qemubox
    echo -e "    ${GREEN}✓${NC} /var/lib/qemubox removed"
fi

# Remove runtime directories
if [ -d /run/qemubox ]; then
    echo "  → Removing /run/qemubox..."
    rm -rf /run/qemubox
    echo -e "    ${GREEN}✓${NC} /run/qemubox removed"
fi

if [ -d /var/run/qemubox ]; then
    echo "  → Removing /var/run/qemubox..."
    rm -rf /var/run/qemubox
    echo -e "    ${GREEN}✓${NC} /var/run/qemubox removed"
fi

# Remove socket if it exists
if [ -S /var/run/qemubox/containerd.sock ]; then
    echo "  → Removing containerd socket..."
    rm -f /var/run/qemubox/containerd.sock
    echo -e "    ${GREEN}✓${NC} Socket removed"
fi

echo ""
echo "================================================"
echo -e "${GREEN}✓ Uninstallation Complete!${NC}"
echo "================================================"
echo ""
echo "All Qemubox components have been removed from your system."
echo ""
