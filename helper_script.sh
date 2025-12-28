cat > run_vm.sh << 'EOF'
#!/bin/bash
SOCKET="/tmp/firecracker.sock"

# Cleanup any existing socket
rm -f "$SOCKET"

# Run Firecracker
firecracker --api-sock "$SOCKET" --config-file test_config.json

# Cleanup after exit
rm -f "$SOCKET"
EOF

chmod +x run_vm.sh

# Run with sudo (since firecracker needs root)
sudo ./run_vm.sh