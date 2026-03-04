  #!/bin/bash                                                                                                                                                         
  cd /Users/abhishekdadwal/nothing/sandbox_env/backend                                                                                                                  
  sudo ASSETS_PATH=/Users/abhishekdadwal/nothing/sandbox_env/assets \
       FIRECRACKER_BINARY=/Users/abhishekdadwal/nothing/sandbox_env/release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64 \
       go run cmd/api/main.go
  EOF
  chmod +x /Users/abhishekdadwal/nothing/sandbox_env/dev.sh
