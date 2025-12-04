#!/bin/bash
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "================================================"
echo "  Beacon Uninstallation Script"
echo "================================================"
echo ""

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}Error: This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

# Confirmation prompt
echo -e "${YELLOW}WARNING: This will remove all Beacon components and data.${NC}"
echo "This includes:"
echo "  - All binaries in /usr/share/beacon"
echo "  - All configuration files in /usr/share/beacon/config"
echo "  - All state data in /var/lib/beacon (containers, images, etc.)"
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
if systemctl is-active --quiet beacon-containerd; then
    echo "  → Stopping beacon-containerd service..."
    systemctl stop beacon-containerd
    echo -e "    ${GREEN}✓${NC} beacon-containerd stopped"
fi

if systemctl is-enabled --quiet beacon-containerd 2>/dev/null; then
    echo "  → Disabling beacon-containerd service..."
    systemctl disable beacon-containerd
    echo -e "    ${GREEN}✓${NC} beacon-containerd disabled"
fi

if systemctl is-active --quiet beacon-buildkit; then
    echo "  → Stopping beacon-buildkit service..."
    systemctl stop beacon-buildkit
    echo -e "    ${GREEN}✓${NC} beacon-buildkit stopped"
fi

if systemctl is-enabled --quiet beacon-buildkit 2>/dev/null; then
    echo "  → Disabling beacon-buildkit service..."
    systemctl disable beacon-buildkit
    echo -e "    ${GREEN}✓${NC} beacon-buildkit disabled"
fi

echo ""
echo "🗑️  Removing files..."

# Remove systemd service symlinks
if [ -L /etc/systemd/system/beacon-containerd.service ]; then
    echo "  → Removing beacon-containerd.service symlink..."
    rm -f /etc/systemd/system/beacon-containerd.service
    echo -e "    ${GREEN}✓${NC} beacon-containerd.service symlink removed"
fi

if [ -L /etc/systemd/system/beacon-buildkit.service ]; then
    echo "  → Removing beacon-buildkit.service symlink..."
    rm -f /etc/systemd/system/beacon-buildkit.service
    echo -e "    ${GREEN}✓${NC} beacon-buildkit.service symlink removed"
fi

systemctl daemon-reload
echo -e "  ${GREEN}✓${NC} Systemd reloaded"

# Remove binaries and configuration
if [ -d /usr/share/beacon ]; then
    echo "  → Removing /usr/share/beacon..."
    rm -rf /usr/share/beacon
    echo -e "    ${GREEN}✓${NC} /usr/share/beacon removed"
fi

# Remove state data
if [ -d /var/lib/beacon ]; then
    echo "  → Removing /var/lib/beacon..."
    rm -rf /var/lib/beacon
    echo -e "    ${GREEN}✓${NC} /var/lib/beacon removed"
fi

# Remove runtime directories
if [ -d /run/beacon ]; then
    echo "  → Removing /run/beacon..."
    rm -rf /run/beacon
    echo -e "    ${GREEN}✓${NC} /run/beacon removed"
fi

if [ -d /var/run/beacon ]; then
    echo "  → Removing /var/run/beacon..."
    rm -rf /var/run/beacon
    echo -e "    ${GREEN}✓${NC} /var/run/beacon removed"
fi

# Remove socket if it exists
if [ -S /var/run/beacon/containerd.sock ]; then
    echo "  → Removing containerd socket..."
    rm -f /var/run/beacon/containerd.sock
    echo -e "    ${GREEN}✓${NC} Socket removed"
fi

echo ""
echo "================================================"
echo -e "${GREEN}✓ Uninstallation Complete!${NC}"
echo "================================================"
echo ""
echo "All Beacon components have been removed from your system."
echo ""
