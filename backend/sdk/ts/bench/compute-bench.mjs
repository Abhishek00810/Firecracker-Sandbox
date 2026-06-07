#!/usr/bin/env node

import { performance } from 'node:perf_hooks';
import { compute } from 'computesdk';
import { sandboxProvider } from '../dist/index.mjs';

const baseUrl = process.env.SANDBOX_BASE_URL ?? 'http://localhost:8080';
const apiKey = process.env.SANDBOX_API_KEY ?? '';
const tier = process.env.SANDBOX_TIER ?? 'pro';
const command = process.env.BENCH_COMMAND ?? 'python -c "print(1+1)"';
const sequentialRuns = intEnv('BENCH_SEQUENTIAL_RUNS', 10);
const hotRuns = intEnv('BENCH_HOT_RUNS', 25);
const concurrencyLevels = listEnv('BENCH_CONCURRENCY', '1,4,8,16,32,50');
const roundsPerConcurrency = intEnv('BENCH_ROUNDS', 1);
const timeoutMs = intEnv('BENCH_TIMEOUT_MS', 120_000);

if (!apiKey) {
  console.error('Missing SANDBOX_API_KEY');
  process.exit(1);
}

compute.setConfig({
  provider: sandboxProvider({ apiKey, baseUrl, tier }),
});

console.log('ComputeSDK sandbox benchmark');
console.log(JSON.stringify({
  baseUrl,
  tier,
  command,
  sequentialRuns,
  hotRuns,
  concurrencyLevels,
  roundsPerConcurrency,
  timeoutMs,
}, null, 2));

await assertHealthy();

const sequential = await runSequentialCreateRunDestroy();
printSection('sequential create -> first run -> destroy', sequential);

const hot = await runHotRunCommand();
printSection('hot runCommand on one sandbox', hot);

const concurrent = [];
for (const concurrency of concurrencyLevels) {
  const samples = [];
  for (let round = 0; round < roundsPerConcurrency; round++) {
    const batch = await runConcurrentCreateRunDestroy(concurrency);
    samples.push(...batch);
  }
  const summary = summarizeSamples(samples);
  concurrent.push({ concurrency, ...summary });
  printConcurrent(concurrency, summary);
}

console.log('\nJSON_RESULT');
console.log(JSON.stringify({
  config: {
    baseUrl,
    tier,
    command,
    sequentialRuns,
    hotRuns,
    concurrencyLevels,
    roundsPerConcurrency,
    timeoutMs,
  },
  sequential: summarizeSamples(sequential),
  hot: summarizeSamples(hot),
  concurrent,
}, null, 2));

async function runSequentialCreateRunDestroy() {
  const samples = [];
  for (let i = 0; i < sequentialRuns; i++) {
    samples.push(await createRunDestroySample(`seq-${i}`));
  }
  return samples;
}

async function runHotRunCommand() {
  const sandbox = await time('createMs', () => compute.sandbox.create({ timeout: timeoutMs }));
  const samples = [];
  try {
    for (let i = 0; i < hotRuns; i++) {
      const run = await time('runMs', () => sandbox.value.runCommand(command, { timeout: timeoutMs }));
      samples.push({
        name: `hot-${i}`,
        ok: run.ok && run.value.exitCode === 0,
        createMs: 0,
        runMs: run.ms,
        destroyMs: 0,
        totalMs: run.ms,
        exitCode: run.value?.exitCode,
        stdout: compact(run.value?.stdout),
        error: run.error,
      });
    }
  } finally {
    await sandbox.value?.destroy().catch(() => {});
  }
  return samples;
}

async function runConcurrentCreateRunDestroy(concurrency) {
  const started = performance.now();
  const samples = await Promise.all(
    Array.from({ length: concurrency }, (_, i) => createRunDestroySample(`c${concurrency}-${i}`)),
  );
  const wallMs = performance.now() - started;
  return samples.map((sample) => ({ ...sample, batchWallMs: wallMs }));
}

async function createRunDestroySample(name) {
  const totalStarted = performance.now();
  let sandbox;
  let createMs = 0;
  let runMs = 0;
  let destroyMs = 0;
  let exitCode;
  let stdout = '';
  let error = '';
  let ok = false;

  try {
    const create = await time('createMs', () => compute.sandbox.create({ timeout: timeoutMs }));
    createMs = create.ms;
    if (!create.ok) throw new Error(create.error);
    sandbox = create.value;

    const run = await time('runMs', () => sandbox.runCommand(command, { timeout: timeoutMs }));
    runMs = run.ms;
    if (!run.ok) throw new Error(run.error);
    exitCode = run.value.exitCode;
    stdout = compact(run.value.stdout);
    ok = exitCode === 0;
    error = ok ? '' : `exitCode=${exitCode}`;
  } catch (err) {
    ok = false;
    error = err?.message ?? String(err);
  } finally {
    if (sandbox) {
      const destroy = await time('destroyMs', () => sandbox.destroy());
      destroyMs = destroy.ms;
    }
  }

  return {
    name,
    ok,
    createMs,
    runMs,
    destroyMs,
    totalMs: performance.now() - totalStarted,
    exitCode,
    stdout,
    error,
  };
}

async function time(_name, fn) {
  const started = performance.now();
  try {
    const value = await fn();
    return { ok: true, value, ms: performance.now() - started };
  } catch (err) {
    return { ok: false, error: err?.message ?? String(err), ms: performance.now() - started };
  }
}

async function assertHealthy() {
  const res = await fetch(`${baseUrl}/health`, { signal: AbortSignal.timeout(10_000) });
  if (!res.ok) throw new Error(`Health check failed: HTTP ${res.status}`);
}

function summarizeSamples(samples) {
  const oks = samples.filter((s) => s.ok);
  return {
    count: samples.length,
    success: oks.length,
    failure: samples.length - oks.length,
    successRate: samples.length ? +(oks.length / samples.length).toFixed(3) : 0,
    createMs: stats(oks.map((s) => s.createMs).filter((n) => n > 0)),
    runMs: stats(oks.map((s) => s.runMs).filter((n) => n > 0)),
    destroyMs: stats(oks.map((s) => s.destroyMs).filter((n) => n > 0)),
    totalMs: stats(oks.map((s) => s.totalMs).filter((n) => n > 0)),
    batchWallMs: stats(samples.map((s) => s.batchWallMs).filter((n) => n > 0)),
    errors: topErrors(samples.filter((s) => !s.ok).map((s) => s.error)),
  };
}

function stats(values) {
  if (!values.length) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return {
    min: round(sorted[0]),
    p50: round(percentile(sorted, 0.50)),
    p95: round(percentile(sorted, 0.95)),
    p99: round(percentile(sorted, 0.99)),
    max: round(sorted[sorted.length - 1]),
    mean: round(sorted.reduce((a, b) => a + b, 0) / sorted.length),
  };
}

function percentile(sorted, p) {
  const idx = Math.min(sorted.length - 1, Math.ceil(sorted.length * p) - 1);
  return sorted[idx];
}

function topErrors(errors) {
  const counts = new Map();
  for (const error of errors) counts.set(error, (counts.get(error) ?? 0) + 1);
  return [...counts.entries()]
    .sort((a, b) => b[1] - a[1])
    .slice(0, 5)
    .map(([error, count]) => ({ count, error }));
}

function printSection(title, samples) {
  console.log(`\n${title}`);
  console.log(JSON.stringify(summarizeSamples(samples), null, 2));
}

function printConcurrent(concurrency, summary) {
  console.log(`\nconcurrency=${concurrency}`);
  console.log(JSON.stringify(summary, null, 2));
}

function compact(value) {
  return String(value ?? '').trim().slice(0, 120);
}

function round(value) {
  return Math.round(value * 10) / 10;
}

function intEnv(name, fallback) {
  const raw = process.env[name];
  if (!raw) return fallback;
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function listEnv(name, fallback) {
  return String(process.env[name] ?? fallback)
    .split(',')
    .map((s) => Number.parseInt(s.trim(), 10))
    .filter((n) => Number.isFinite(n) && n > 0);
}
