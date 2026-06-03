import { defineProvider } from '@computesdk/provider';
import type {
  CommandResult,
  SandboxInfo,
  CreateSandboxOptions,
  RunCommandOptions,
} from '@computesdk/provider';

export interface SandboxConfig {
  apiKey?: string;
  baseUrl?: string;
  tier?: string;
}

interface SandboxHandle {
  sessionId: string;
  baseUrl: string;
  apiKey: string;
}

async function apiFetch(
  baseUrl: string,
  apiKey: string,
  path: string,
  options: RequestInit = {},
): Promise<Response> {
  const res = await fetch(`${baseUrl}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${apiKey}`,
      ...options.headers,
    },
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new Error(`API error ${res.status}: ${text}`);
  }
  return res;
}

export const sandboxProvider = defineProvider<SandboxHandle, SandboxConfig>({
  name: 'sandbox',
  methods: {
    sandbox: {
      create: async (config: SandboxConfig, _options?: CreateSandboxOptions) => {
        const apiKey = config.apiKey ?? process.env.SANDBOX_API_KEY ?? '';
        const baseUrl = config.baseUrl ?? process.env.SANDBOX_BASE_URL ?? 'http://localhost:8080';
        const tier = config.tier ?? 'pro';

        if (!apiKey) throw new Error('Missing SANDBOX_API_KEY');

        const res = await apiFetch(baseUrl, apiKey, '/session', {
          method: 'POST',
          body: JSON.stringify({ tier }),
        });

        const data = await res.json();
        const sessionId: string = data.session?.session_id ?? data.session_id;
        if (!sessionId) throw new Error('No session_id in create response');

        const sandbox: SandboxHandle = { sessionId, baseUrl, apiKey };
        return { sandbox, sandboxId: sessionId };
      },

      getById: async (config: SandboxConfig, sandboxId: string) => {
        const apiKey = config.apiKey ?? process.env.SANDBOX_API_KEY ?? '';
        const baseUrl = config.baseUrl ?? process.env.SANDBOX_BASE_URL ?? 'http://localhost:8080';

        try {
          const res = await apiFetch(baseUrl, apiKey, `/session/${sandboxId}`);
          const data = await res.json();
          const sessionId: string = data.session?.session_id ?? sandboxId;
          const sandbox: SandboxHandle = { sessionId, baseUrl, apiKey };
          return { sandbox, sandboxId: sessionId };
        } catch {
          return null;
        }
      },

      list: async (_config: SandboxConfig) => {
        return [];
      },

      destroy: async (config: SandboxConfig, sandboxId: string) => {
        const apiKey = config.apiKey ?? process.env.SANDBOX_API_KEY ?? '';
        const baseUrl = config.baseUrl ?? process.env.SANDBOX_BASE_URL ?? 'http://localhost:8080';

        await apiFetch(baseUrl, apiKey, `/session/${sandboxId}`, {
          method: 'DELETE',
        }).catch(() => {});
      },

      runCommand: async (
        sandbox: SandboxHandle,
        command: string,
        options?: RunCommandOptions,
      ): Promise<CommandResult> => {
        const timeout = options?.timeout ? Math.ceil(options.timeout / 1000) : 30;
        const startTime = Date.now();

        const res = await apiFetch(sandbox.baseUrl, sandbox.apiKey, `/session/${sandbox.sessionId}/exec`, {
          method: 'POST',
          body: JSON.stringify({ command, timeout }),
        });

        const data = await res.json();
        const result = data.result ?? {};

        return {
          stdout: result.stdout ?? '',
          stderr: result.stderr ?? '',
          exitCode: result.exit_code ?? 0,
          durationMs: Date.now() - startTime,
        };
      },

      getInfo: async (sandbox: SandboxHandle): Promise<SandboxInfo> => {
        const res = await apiFetch(sandbox.baseUrl, sandbox.apiKey, `/session/${sandbox.sessionId}`);
        const data = await res.json();

        return {
          id: sandbox.sessionId,
          provider: 'sandbox',
          status: data.session?.state === 'active' ? 'running' : 'stopped',
          createdAt: new Date(data.session?.created_at ?? Date.now()),
          timeout: 900000,
        };
      },

      getUrl: async (_sandbox: SandboxHandle, _options: { port: number; protocol?: string }) => {
        throw new Error('getUrl not supported');
      },

      getInstance: (sandbox: SandboxHandle) => sandbox,
    },
  },
});
