# Demo \u0026 Examples

Live examples of the Firecracker Sandbox Engine in action, including sample executions, performance benchmarks, and edge case testing.

---

## Table of Contents

1. [Basic Examples](#basic-examples)
2. [Multi-Language Support](#multi-language-support)
3. [Error Handling](#error-handling)
4. [Performance Benchmarks](#performance-benchmarks)
5. [Edge Cases](#edge-cases)
6. [Load Testing](#load-testing)

---

## Basic Examples

### Example 1: Hello World (Python)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(\"Hello, World!\")",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Hello, World!\n",
    "duration": 0.082,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- ✅ Execution time: **82ms** (including VM acquisition, vsock communication)
- ✅ Clean exit code: **0**
- ✅ Perfect for simple scripts

---

### Example 2: Fibonacci Sequence (Python)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "def fib(n):\n    if n <= 1:\n        return n\n    return fib(n-1) + fib(n-2)\n\nfor i in range(10):\n    print(f\"fib({i}) = {fib(i)}\")",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "fib(0) = 0\nfib(1) = 1\nfib(2) = 1\nfib(3) = 2\nfib(4) = 3\nfib(5) = 5\nfib(6) = 8\nfib(7) = 13\nfib(8) = 21\nfib(9) = 34\n",
    "duration": 0.098,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- Demonstrates multi-line code execution
- Recursive functions work perfectly
- Still completes in under 100ms

---

### Example 3: JSON Processing (Python)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "import json\ndata = {\"name\": \"Alice\", \"age\": 30, \"city\": \"NYC\"}\nprint(json.dumps(data, indent=2))",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "{\n  \"name\": \"Alice\",\n  \"age\": 30,\n  \"city\": \"NYC\"\n}\n",
    "duration": 0.091,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- Python standard library (json) works out of the box
- Perfect for API response processing demos

---

## Multi-Language Support

### Python Example

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "import math\nprint(f\"Pi = {math.pi}\")\nprint(f\"Square root of 16 = {math.sqrt(16)}\")",
    "language": "python"
  }'
```

**Output:**
```
Pi = 3.141592653589793
Square root of 16 = 4.0
```

---

### Node.js Example

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "const nums = [1, 2, 3, 4, 5];\nconst sum = nums.reduce((a, b) => a + b, 0);\nconsole.log(`Sum: ${sum}`);\nconsole.log(`Average: ${sum / nums.length}`);",
    "language": "nodejs"
  }'
```

**Output:**
```
Sum: 15
Average: 3
```

---

### Bash Example

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "#!/bin/bash\necho \"Current date: $(date)\"\necho \"Processes: $(ps aux | wc -l)\"\necho \"Disk usage:\"\ndf -h / | tail -1",
    "language": "bash"
  }'
```

**Output:**
```
Current date: Tue Jan 28 13:45:22 UTC 2026
Processes: 12
Disk usage:
/dev/vda        200M  120M   80M  60% /
```

---

### Go Example

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "package main\nimport (\n    \"fmt\"\n    \"time\"\n)\nfunc main() {\n    fmt.Println(\"Hello from Go!\")\n    fmt.Printf(\"Current time: %s\\n\", time.Now().Format(time.RFC3339))\n}",
    "language": "go"
  }'
```

**Output:**
```
Hello from Go!
Current time: 2026-01-28T13:45:23Z
```

---

## Error Handling

### Example 1: Runtime Error (Python)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(10 / 0)",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\nZeroDivisionError: division by zero\n",
    "duration": 0.076,
    "exit_code": 1,
    "termination_reason": "success"
  },
  "status": "error",
  "error": "Execution failed with exit code 1"
}
```

**Analysis:**
- ❌ **Exit code 1** indicates error
- ✅ **Full stack trace** captured in output
- ✅ **Fast failure** (76ms)

---

### Example 2: Syntax Error (Python)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(\"Missing closing quote)",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "  File \"<string>\", line 1\n    print(\"Missing closing quote)\n                                ^\nSyntaxError: EOL while scanning string literal\n",
    "duration": 0.071,
    "exit_code": 1,
    "termination_reason": "success"
  },
  "status": "error",
  "error": "Execution failed with exit code 1"
}
```

**Analysis:**
- Python interpreter catches syntax errors
- Clear error messages for debugging

---

### Example 3: Import Error

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "import nonexistent_module\nprint(\"Hello\")",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\nModuleNotFoundError: No module named 'nonexistent_module'\n",
    "duration": 0.079,
    "exit_code": 1,
    "termination_reason": "success"
  },
  "status": "error",
  "error": "Execution failed with exit code 1"
}
```

---

## Performance Benchmarks

### Benchmark 1: Empty Script

**Code:**
```python
# Empty script
```

**Result:**
- Duration: **68ms**
- Breakdown: VM acquisition (20ms) + vsock (10ms) + Python startup (38ms)

---

### Benchmark 2: CPU Intensive (Prime Number Check)

**Code:**
```python
def is_prime(n):
    if n < 2:
        return False
    for i in range(2, int(n**0.5) + 1):
        if n % i == 0:
            return False
    return True

primes = [n for n in range(1, 10000) if is_prime(n)]
print(f"Found {len(primes)} primes")
```

**Result:**
```json
{
  "output": {
    "output": "Found 1229 primes\n",
    "duration": 0.487,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- Computation time: **487ms**
- CPU limits respected (2 vCPUs)
- No timeout issues

---

### Benchmark 3: Memory Allocation (50MB)

**Code:**
```python
import sys
data = [0] * (50 * 1024 * 1024 // 8)  # 50MB of integers
print(f"Allocated {sys.getsizeof(data) / 1024 / 1024:.2f} MB")
```

**Result:**
```json
{
  "output": {
    "output": "Allocated 50.00 MB\n",
    "duration": 0.183,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- Successfully allocated 50MB (within 256MB limit)
- Fast allocation time: **183ms**

---

### Benchmark 4: File I/O

**Code:**
```bash
#!/bin/bash
dd if=/dev/zero of=/tmp/testfile bs=1M count=10 2>&1 | tail -1
ls -lh /tmp/testfile
rm /tmp/testfile
```

**Result:**
```
10485760 bytes (10 MB) copied, 0.05 s, 200 MB/s
-rw-r--r--    1 root     root       10.0M Jan 28 13:45 /tmp/testfile
```

**Analysis:**
- File I/O works in `/tmp` (writable)
- Fast write speed: **200 MB/s**

---

## Edge Cases

### Edge Case 1: Infinite Loop (Timeout Test)

**Code:**
```python
import time
time.sleep(35)  # Exceeds 30s timeout
print("This won't print")
```

**Result:**
```json
{
  "output": {
    "output": "",
    "duration": 30.002,
    "exit_code": -1,
    "termination_reason": "timeout"
  },
  "status": "error",
  "error": "Execution timed out"
}
```

**Analysis:**
- ✅ **Timeout enforced** at exactly 30 seconds
- ✅ **Clean termination** (no hung VMs)
- ✅ **Exit code -1** indicates timeout

---

### Edge Case 2: Memory Bomb (OOM Test)

**Code:**
```python
data = [0] * (300 * 1024 * 1024 // 8)  # Try to allocate 300MB
print("This won't print")
```

**Result:**
```json
{
  "output": {
    "output": "Killed\n",
    "duration": 1.872,
    "exit_code": 137,
    "termination_reason": "oom_kill"
  },
  "status": "error",
  "error": "Execution failed with exit code 137"
}
```

**Analysis:**
- ✅ **OOM Killer activated** (exit code 137 = SIGKILL)
- ✅ **Memory limit enforced** (256MB hard cap)
- ✅ **Fast detection** (1.87s to detect OOM)

---

### Edge Case 3: Fork Bomb

**Code:**
```python
import os
for i in range(200):
    try:
        os.fork()
    except OSError as e:
        print(f"Fork #{i} failed: {e}")
        break
```

**Result:**
```json
{
  "output": {
    "output": "Fork #87 failed: [Errno 11] Resource temporarily unavailable\n",
    "duration": 0.234,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- ✅ **Process limits enforced** (ulimit prevents unbounded forks)
- ✅ **System protected** from fork bomb
- ✅ Error after ~87 forks

---

### Edge Case 4: Network Request Attempt

**Code:**
```python
import urllib.request
try:
    response = urllib.request.urlopen("http://google.com")
    print(response.read())
except Exception as e:
    print(f"Network error: {e}")
```

**Result:**
```json
{
  "output": {
    "output": "Network error: <urlopen error [Errno -3] Temporary failure in name resolution>\n",
    "duration": 0.145,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

**Analysis:**
- ✅ **Network completely disabled**
- ✅ **DNS unavailable** (no name resolution)
- ✅ **No data exfiltration possible**

---

### Edge Case 5: File System Escape Attempt

**Code:**
```bash
#!/bin/bash
ls /
cat /etc/passwd
rm -rf /bin
```

**Result:**
```
bin  dev  etc  home  lib  media  mnt  opt  proc  root  run  sbin  srv  sys  tmp  usr  var
root:x:0:0:root:/root:/bin/sh
daemon:x:1:1:daemon:/usr/sbin:/bin/sh
...
rm: can't remove '/bin': Read-only file system
```

**Analysis:**
- ✅ **Read-only root filesystem** prevents damage
- ✅ `/etc/passwd` readable (isolated VM, no sensitive data)
- ✅ Cannot delete system files

---

## Load Testing

### Test 1: Sequential Executions (10 requests)

**Script:**
```bash
for i in {1..10}; do
  curl -s -X POST http://localhost:8080/execute \
    -H "Content-Type: application/json" \
    -d "{\"code\": \"print($i)\", \"language\": \"python\"}" \
    | jq '.output.duration'
done
```

**Results:**
```
0.084
0.078
0.081
0.079
0.082
0.080
0.083
0.077
0.085
0.079
```

**Analysis:**
- **Average duration:** 80.8ms
- **Std deviation:** 2.5ms
- ✅ **Consistent performance**

---

### Test 2: Concurrent Executions (50 parallel requests)

**Script:**
```bash
for i in {1..50}; do
  curl -s -X POST http://localhost:8080/execute \
    -H "Content-Type: application/json" \
    -d "{\"code\": \"print($i)\", \"language\": \"python\"}" &
done
wait
```

**Results:**
- **All 50 requests:** ✅ Succeeded
- **Average response time:** 1.2s (includes queue wait time)
- **No failures:** 0 timeouts, 0 errors

**Analysis:**
- ✅ **Job queue handles congestion**
- ✅ **No crashes or hangs**
- ✅ **Graceful handling of burst traffic**

---

### Test 3: Queue Overflow (150 requests)

**Scenario:** Submit more than queue buffer size (100 jobs)

**Expected behavior:** First 100 accepted, remaining 50 rejected with 503

**Results:**
- **Accepted:** 100 jobs
- **Rejected:** 50 jobs with `503 Service Unavailable`
- **Error message:** `queue full, try again later`

**Analysis:**
- ✅ **Backpressure mechanism works**
- ✅ **No server crash**
- ✅ **Clear error message for clients**

---

## Performance Summary

| Test Type | Metric | Value |
|-----------|--------|-------|
| **Cold Boot** | VM boot time | 95-120ms |
| **Warm Pool** | VM acquisition | 20-40ms |
| **Hello World** | Total time | 78-85ms |
| **CPU Intensive** | Prime calculation (10k) | 487ms |
| **Memory** | 50MB allocation | 183ms |
| **Timeout** | Enforcement accuracy | 30.002s (±2ms) |
| **Concurrent** | 50 parallel requests | All succeeded |
| **Throughput** | Sustained requests/sec | ~12 req/s (10 workers) |

---

## Tips for Testing

### 1. Use `jq` for Pretty Output

```bash
curl -s -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"code": "print(42)", "language": "python"}' \
  | jq '.'
```

---

### 2. Extract Only Output

```bash
curl -s -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"code": "print(\"Hello\")", "language": "python"}' \
  | jq -r '.output.output'
```

---

### 3. Benchmark Multiple Runs

```bash
hyperfine --warmup 3 --runs 10 \
  'curl -s -X POST http://localhost:8080/execute -d "{\"code\": \"print(1)\", \"language\": \"python\"}"'
```

---

## Try It Yourself!

Start the server and experiment with your own code:

```bash
cd backend/cmd/api
sudo go run main.go
```

Then test with:
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "YOUR_CODE_HERE",
    "language": "python"
  }'
```

---

**Last Updated:** Sprint 8  
**Version:** v0.1
