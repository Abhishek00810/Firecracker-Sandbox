# E2B Architecture for Secure Remote Code Execution (Sandbox Engine)

## Why Running User Code Directly on a Server is Dangerous

The main problem is lack of isolation can cause problems to the entire server. Even a single piece of code which can cause TLE (Time Limit Exceeded) can lag up the entire server and cause issues for all users. A single infected code can create problems for all users who are working under the server, which is dangerous. Directly working on a server sometimes can cause malicious code to run which can delete important files by using `rm` command. This must be prevented.

## What Problems E2B Architecture Solves

- **Isolation**: Helps to prevent outage for all users if a single TLE occurs
- **Speed**: MicroVMs boot really fast under 100ms
- **Security**: All MicroVMs are independent of each other and destroyed after each request
- **Resources**: No need for high maintainable VMs, these are lightweight

## System Overview (High-Level Flow)

**Without mentioning Docker or Firecracker:**

1. Client submits code with language → 
2. (Go) API validates the request → 
3. Job is queued → 
4. Worker pool takes the task → 
5. Code executes by worker node (within limits) → 
6. Output is collected → 
7. VM is destroyed

## Core Components & Responsibilities

### API Layer

**Responsibilities:** 
API Layer is the middleware between the executor and the user itself. This will make sure request is authenticated and is under rate limiting so that resources are not doing any work without proper authorization.

**What it must NOT do:** 
- It should not directly talk with MicroVM
- API Layer should not make any unauthenticated user allow to run code inside the sandbox

### Job Queue / Scheduler

**Why it exists:** 
It exists so that a number of requests can be performed with limited concurrent worker nodes. If the worker nodes are already taken up by the jobs, the job queue will make sure the requests are getting awaited and not getting destroyed in the meanwhile. It will make sure each request/job is getting a worker node after the wait. It also makes sure to work within buffer allotted to it - if exceeded, no request will be fulfilled (happens during peak hours).

**Implementation Details:**
- **Queue Type**: Buffered channel (100 jobs capacity)
- **Worker Pool**: Fixed size (10 workers by default, configurable)
- **Timeout**: 12 seconds per job (enforced via context)
- **Backpressure**: Returns error when queue is full (`queue full, try again later`)

**How it protects the system:** 
- It makes sure that each request gets to a worker node instead of getting lost - it saves the request
- It ensures which worker node is getting lesser work and provides it to it, otherwise some worker nodes can get heavy work which can cause CPU/memory OOM_KILLER overhead and kill the nodes over time
- **Prevents resource exhaustion**: Queue buffer prevents unbounded memory growth
- **Fair scheduling**: FIFO order ensures fairness
- **Graceful degradation**: Returns error instead of crashing when overloaded

**Future Improvements:**
- Priority queue for different job types
- Dynamic worker scaling based on load
- Per-language worker pools
- Job cancellation support

### Executor (Sandbox Runtime)

**Contract it must satisfy:**
- Isolation and security
- Performance and efficiency
- Reliability and maintenance

**Core Interface:**
```go
type Executor interface {
    Execute(ctx context.Context, code string, language string) (ExecutionResult, error)
}
```

**Important Design Principle:**
> Runtime-specific preparation steps such as Docker image pulls or VM rootfs setup are encapsulated within the executor implementation and are not exposed via the executor interface.

**Implementation-Specific Methods (NOT in interface):**
- **DockerExecutor**: `ensureImage()` (private method, called internally)
- **FirecrackerExecutor**: `ensureRootFS()`, `bootVM()` (private methods, called internally)

**VM Lifecycle (Firecracker):**
1. **Create**: Initialize microVM with kernel + rootfs
2. **Configure**: Set resource limits (CPU, memory, disk)
3. **Inject Code**: Write code to VM filesystem or pass via stdin
4. **Execute**: Run code with language-specific runtime
5. **Collect Output**: Capture stdout/stderr via serial console or log driver
6. **Destroy**: Force kill and cleanup VM resources

**Resource Limits (per execution):**
- **Memory**: 128MB (Docker) → 256MB (Firecracker target)
- **CPU**: 0.5 vCPU (Docker) → 1-2 vCPU (Firecracker)
- **Timeout**: 12 seconds (configurable)
- **Process Limit**: 20 processes max
- **File Size**: 10MB max
- **Network**: Completely disabled

**Code Injection Strategy:**
- **Docker**: Pass code via command-line (`python -c "code"`)
- **Firecracker**: Write to `/tmp/user_code.py` and execute, or pass via stdin
- **Future**: Use snapshots with pre-loaded language runtimes

**Why it's replaceable:** 
It is replaceable due to many reasons which include how the code engine needs to execute - whether Docker or Firecracker. Both implementations must satisfy the same interface contract.

### Isolation Boundary

**Threat model:** 
It helps to make sure three layers:
- **Layer 1: API Request** - Input validation, rate limiting, authentication
- **Layer 2: Linux Server/Executor** - Host-level security (seccomp, capabilities, namespaces)
- **Layer 3: MiniVMs** - Complete hardware-level isolation (Firecracker) or container isolation (Docker)

**Security Hardening:**

**Docker (Current):**
- Network disabled (`NetworkMode: "none"`)
- Read-only root filesystem
- All capabilities dropped (`CapDrop: ["ALL"]`)
- Non-privileged user (`User: "1000"`)
- Process limits (PIDs, file descriptors)
- No new privileges (`no-new-privileges`)
- Resource limits (memory, CPU, file size)

**Firecracker (Target):**
- Hardware-level virtualization (KVM)
- No shared kernel with host
- Minimal attack surface (microVM)
- Seccomp filters for API calls
- Jailer for additional isolation (optional)
- No network by default
- Ephemeral VMs (destroyed after execution)

**What isolation guarantees:** 
Isolation guarantees that each code will be executed in their own dedicated small environment given to the user and it won't affect other people's code or the server which can cause downtime if malicious code starts to replicate. Each execution is completely isolated with no shared state between runs.

### Result Channel

**Allowed outputs:** 
- **stdout**: Standard output from code execution
- **stderr**: Standard error output
- **Execution speed**: Duration in seconds (float64)
- **Exit codes**: Process exit code (0 = success, non-zero = error)
- **Termination reason**: Why execution stopped (`success`, `timeout`, `oom_kill`, `runtime_error`)

**Result Structure:**
```go
type ExecutionResult struct {
    Output            string  `json:"output"`              // Combined stdout+stderr
    Duration          float64 `json:"duration"`           // Execution time in seconds
    ExitCode          int64   `json:"exit_code"`          // Process exit code
    TerminationReason string  `json:"termination_reason"` // Why execution stopped
}
```

**Error Handling:**
- **Timeout**: Context deadline exceeded → `TerminationReason: "timeout"`
- **OOM Kill**: Memory limit exceeded → `ExitCode: 137`, `TerminationReason: "oom_kill"`
- **Runtime Error**: Code execution failed → `ExitCode: non-zero`, `TerminationReason: "runtime_error"`
- **System Error**: Executor failure → Returns error, no result

**Why it's one-way:** 
So that no malicious code can generate network callbacks or shared memory. The VM/container has no network access and cannot communicate back except through the controlled output channel.

---

## Implementation Notes

This architecture is inspired by E2B's approach to secure code execution. The system is designed to be executor-agnostic, allowing for seamless transition between Docker (current implementation) and Firecracker (target implementation).

## Additional Considerations

### Error Handling & Resilience

**Failure Modes:**
- **Queue Full**: Return HTTP 503, client should retry
- **Worker Crash**: Job is lost, client must resubmit (future: persistent queue)
- **VM Creation Failure**: Return error, don't retry automatically
- **Timeout**: Kill execution, return timeout result
- **OOM**: VM/container killed, return OOM result

**Cleanup Guarantees:**
- All VMs/containers are destroyed after execution (defer cleanup)
- Even on panic/error, cleanup runs
- No resource leaks between executions

### Language Support

**Current:** Python only (hardcoded `python:alpine` image)

**Future Multi-Language Strategy:**
- Language-specific VM snapshots (pre-booted with runtime)
- Language detection → select appropriate executor config
- Per-language resource limits (e.g., Go needs more memory)
- Language-specific timeouts

### Observability (Future - Sprint 7)

**Metrics to Track:**
- Execution duration (p50, p95, p99)
- Queue depth
- Worker utilization
- Error rates by type (timeout, OOM, runtime error)
- Resource usage (memory, CPU per execution)

**Logging:**
- Request ID for tracing
- Execution start/end times
- Termination reasons
- Alerts for unexpected failures (e.g., exit code 137 without OOM)

### Scaling Strategy

**Current:** Single instance, fixed worker pool

**Future (Sprint 5):**
- VM pooling for faster boot times
- Horizontal scaling (multiple API instances)
- Load balancing across instances
- Auto-scaling based on queue depth

### Security Considerations

**Input Validation:**
- Code size limits (prevent DoS via large code)
- Language whitelist
- Rate limiting per user/IP
- Authentication/authorization (future)

**Output Sanitization:**
- Truncate output if too large (prevent DoS)
- Sanitize control characters
- Limit output size (e.g., 1MB max)

**Resource Exhaustion Prevention:**
- Queue buffer limits
- Worker pool limits
- Per-execution resource limits
- Global resource quotas (future)

