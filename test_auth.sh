#!/bin/bash
curl -s -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer comp_key_test1234567890abcdef12345678" \
  -d '{"code":"print(1+1)","language":"python"}'
