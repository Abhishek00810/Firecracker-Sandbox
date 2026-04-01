#!/usr/bin/env node
/**
 * node_bridge.js — persistent Node.js execution bridge
 *
 * Same stdin/stdout JSON protocol as kernel_bridge.py:
 *   stdin  → {"code": "const x = 1", "timeout": 30}
 *   stdout ← {"stdout": "", "stderr": "", "exit_code": 0, "duration": 0.01}
 *
 * All executions share the same vm context — variables persist across calls.
 */

'use strict';

const vm      = require('vm');
const readline = require('readline');

function log(msg) {
    process.stderr.write(`[node_bridge] ${msg}\n`);
}

// ── persistent context ────────────────────────────────────────────────────────
// Everything defined here is available to user code across all calls.

const context = vm.createContext({
    require,
    console,
    process,
    setTimeout,
    setInterval,
    clearTimeout,
    clearInterval,
    Buffer,
    URL,
    URLSearchParams,
});

log(`node_bridge ready pid=${process.pid} node=${process.version}`);

// ── request loop ──────────────────────────────────────────────────────────────

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

rl.on('line', (line) => {
    line = line.trim();
    if (!line) return;

    let req;
    try {
        req = JSON.parse(line);
    } catch (e) {
        process.stdout.write(JSON.stringify({
            stdout: '', stderr: `invalid json: ${e.message}`, exit_code: 1, duration: 0.0,
        }) + '\n');
        return;
    }

    const code    = req.code    || '';
    const timeout = Math.min(parseInt(req.timeout) || 30, 30) * 1000; // ms

    const start = Date.now();

    // capture stdout by temporarily overriding console.log in the context
    const stdoutParts = [];
    const stderrParts = [];

    const origLog   = context.console.log;
    const origError = context.console.error;
    const origWarn  = context.console.warn;

    context.console.log   = (...args) => { stdoutParts.push(args.map(String).join(' ') + '\n'); };
    context.console.error = (...args) => { stderrParts.push(args.map(String).join(' ') + '\n'); };
    context.console.warn  = (...args) => { stderrParts.push(args.map(String).join(' ') + '\n'); };

    let exit_code = 0;

    try {
        const result = vm.runInContext(code, context, {
            timeout,
            displayErrors: false,
        });

        // if the last expression returns a non-undefined value, print it
        if (result !== undefined) {
            stdoutParts.push(String(result) + '\n');
        }
    } catch (e) {
        if (e.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT') {
            stderrParts.push(`\n[Timeout after ${timeout / 1000}s]\n`);
        } else {
            stderrParts.push(e.stack || e.message);
        }
        exit_code = 1;
    } finally {
        context.console.log   = origLog;
        context.console.error = origError;
        context.console.warn  = origWarn;
    }

    const duration = (Date.now() - start) / 1000;

    process.stdout.write(JSON.stringify({
        stdout:    stdoutParts.join(''),
        stderr:    stderrParts.join(''),
        exit_code,
        duration,
    }) + '\n');
});

rl.on('close', () => {
    log('stdin closed, exiting');
    process.exit(0);
});
