# API Reference

Complete API documentation for the Firecracker Sandbox Engine.

---

## Base URL

```
http://localhost:8080
```

---

## Endpoints

### 1. Health Check

**Endpoint:** `GET /health`

**Description:** Check if the server is running and healthy.

**Request:**
```bash
curl http://localhost:8080/health
```

**Response:**
```json
{
  "status": "ok",
  "message": "Server is healthy and is rocking!!!"
}
```

**Status Codes:**
- `200 OK`: Server is healthy

---

### 2. Execute Code

**Endpoint:** `POST /execute`

**Description:** Execute code in a secure, isolated Firecracker microVM.

#### Request

**Headers:**
```
Content-Type: application/json
```

**Body:**
```json
{
  "code": "string",      // Required: Code to execute
  "language": "string"   // Required: Programming language
}
```

**Supported Languages:**
- `python` - Python 3.x
- `nodejs` - Node.js (latest)
- `bash` - Bash shell
- `go` - Go compiler

#### Response

**Success Response:**
```json
{
  "output": {
    "output": "string",              // Combined stdout + stderr
    "duration": 0.0,                 // Execution time in seconds (float)
    "exit_code": 0,                  // Process exit code (0 = success)
    "termination_reason": "success"  // Why execution stopped
  },
  "status": "success",
  "error": ""                        // Empty on success
}
```

**Error Response:**
```json
{
  "output": {
    "output": "string",
    "duration": 0.0,
    "exit_code": 1,
    "termination_reason": "runtime_error"
  },
  "status": "error",
  "error": "Execution failed with exit code 1"
}
```

#### Termination Reasons

| Reason | Description | Exit Code |
|--------|-------------|-----------|
| `success` | Code executed successfully | 0 |
| `runtime_error` | Code crashed or threw error | Non-zero |
| `timeout` | Exceeded 30-second limit | Varies |
| `oom_kill` | Out of memory (>256MB) | 137 |

#### Status Codes

- `200 OK`: Execution completed (check `status` field for success/error)
- `400 Bad Request`: Invalid JSON or missing required fields
- `503 Service Unavailable`: Queue is full, retry later

---

## Usage Examples

### Python Examples

#### 1. Hello World

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

---

#### 2. Multi-line Python

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "import math\nfor i in range(5):\n    print(f\"Square of {i} = {i**2}\")",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Square of 0 = 0\nSquare of 1 = 1\nSquare of 2 = 4\nSquare of 3 = 9\nSquare of 4 = 16\n",
    "duration": 0.095,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

---

#### 3. Error Handling

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(1/0)",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\nZeroDivisionError: division by zero\n",
    "duration": 0.078,
    "exit_code": 1,
    "termination_reason": "success"
  },
  "status": "error",
  "error": "Execution failed with exit code 1"
}
```

---

### Node.js Examples

#### 1. Console Output

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "console.log(\"Node.js says hello!\");\nconsole.log(Math.PI);",
    "language": "nodejs"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Node.js says hello!\n3.141592653589793\n",
    "duration": 0.112,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

---

#### 2. Array Operations

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "const arr = [1, 2, 3, 4, 5];\nconst doubled = arr.map(x => x * 2);\nconsole.log(doubled);",
    "language": "nodejs"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "[ 2, 4, 6, 8, 10 ]\n",
    "duration": 0.098,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

---

### Bash Examples

#### 1. System Commands

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "#!/bin/bash\necho \"Hostname: $(hostname)\"\necho \"Uptime: $(uptime)\"\nuname -a",
    "language": "bash"
  }'
```

---

#### 2. File Operations

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "#!/bin/bash\necho \"Hello\" > /tmp/test.txt\ncat /tmp/test.txt\nls -lh /tmp/test.txt",
    "language": "bash"
  }'
```

---

### Go Examples

#### 1. Hello World

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "package main\nimport \"fmt\"\nfunc main() {\n    fmt.Println(\"Hello from Go!\")\n}",
    "language": "go"
  }'
```

---

## Error Scenarios

### 1. Invalid JSON

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d 'invalid json'
```

**Response:**
```
HTTP/1.1 400 Bad Request
Invalid request
```

---

### 2. Missing Language Field

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(1+1)"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "",
    "duration": 0,
    "exit_code": 0,
    "termination_reason": ""
  },
  "status": "error",
  "error": "unsupported language: "
}
```

---

### 3. Queue Full

**Scenario:** More than 100 jobs in queue

**Response:**
```
HTTP/1.1 503 Service Unavailable
queue full, try again later
```

**Solution:** Retry the request after a few seconds.

---

### 4. Timeout (>30 seconds)

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "import time\ntime.sleep(35)",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "",
    "duration": 30.001,
    "exit_code": -1,
    "termination_reason": "timeout"
  },
  "status": "error",
  "error": "Execution timed out"
}
```

---

### 5. Memory Limit Exceeded

**Request:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "data = [0] * (300 * 1024 * 1024)",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Killed\n",
    "duration": 2.3,
    "exit_code": 137,
    "termination_reason": "oom_kill"
  },
  "status": "error",
  "error": "Execution failed with exit code 137"
}
```

---

## Rate Limiting

**Current:** No rate limiting implemented

**Future (Sprint 7):**
- Per-IP rate limiting (e.g., 100 requests/minute)
- Authenticated users get higher limits
- 429 Too Many Requests status code

---

## Best Practices

### 1. Handle All Exit Codes

Always check both `status` and `exit_code`:

```javascript
fetch('/execute', {
  method: 'POST',
  body: JSON.stringify({ code: userCode, language: 'python' })
})
.then(res => res.json())
.then(data => {
  if (data.status === "success" && data.output.exit_code === 0) {
    console.log("Success:", data.output.output);
  } else {
    console.error("Failed:", data.error || data.output.output);
  }
});
```

---

### 2. Implement Retry Logic

```python
import requests
import time

def execute_code(code, language, max_retries=3):
    for attempt in range(max_retries):
        try:
            response = requests.post(
                'http://localhost:8080/execute',
                json={'code': code, 'language': language},
                timeout=35  # Slightly more than 30s execution timeout
            )
            
            if response.status_code == 503:  # Queue full
                time.sleep(2 ** attempt)  # Exponential backoff
                continue
                
            return response.json()
        except requests.RequestException as e:
            print(f"Request failed: {e}")
            time.sleep(1)
    
    raise Exception("Max retries exceeded")
```

---

### 3. Validate Input Client-Side

```javascript
function validateCode(code, language) {
  const supportedLanguages = ['python', 'nodejs', 'bash', 'go'];
  
  if (!code || code.trim() === '') {
    throw new Error('Code cannot be empty');
  }
  
  if (!supportedLanguages.includes(language)) {
    throw new Error(`Unsupported language: ${language}`);
  }
  
  if (code.length > 100000) {  // 100KB limit
    throw new Error('Code too large');
  }
}
```

---

## Performance Tips

### Typical Response Times

| Language | Simple Code | Complex Code | Notes |
|----------|-------------|--------------|-------|
| Python | 80-120ms | 500ms-5s | Import-heavy code slower |
| Node.js | 100-150ms | 300ms-3s | Fast startup |
| Bash | 50-80ms | Varies | System command dependent |
| Go | 200-400ms | 1-10s | Compilation time included |

### Optimization Tips

1. **Use VM pool** (already enabled) - Reduces cold start time
2. **Batch requests** - Submit multiple executions concurrently
3. **Cache results** - Identical code → identical output (deterministic)
4. **Minimize code size** - Smaller payloads = faster network transfer

---

## WebSocket Support (Future)

**Planned for v0.2:**

```javascript
const ws = new WebSocket('ws://localhost:8080/execute-stream');

ws.send(JSON.stringify({ code: 'print("Hello")', language: 'python' }));

ws.onmessage = (event) => {
  const data = JSON.parse(event.data);
  console.log("Real-time output:", data.chunk);
};
```

This will enable streaming output for long-running executions.

---

## Security Headers

**Request Headers:**
```
Content-Type: application/json
```

**Response Headers:**
```
Content-Type: application/json
X-Content-Type-Options: nosniff
```

---

## Support

For issues or questions:
- GitHub Issues: [Repository Issues](https://github.com/Abhishek00810/sandbox_env/issues)
- Email: [Your Email]

---

**Last Updated:** Sprint 8  
**API Version:** v0.1
