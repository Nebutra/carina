/**
 * Deterministic JSON-RPC mock responses for the docs playground.
 * Used when mode=mock so readers get a realistic reply without a live daemon.
 */

export type RpcRequest = {
  jsonrpc?: string;
  id?: string | number | null;
  method?: string;
  params?: Record<string, unknown>;
};

export function mockRpcResponse(req: RpcRequest): {
  status: number;
  body: Record<string, unknown>;
  latencyMs: number;
} {
  const id = req.id ?? 1;
  const method = String(req.method || '');
  const params = (req.params && typeof req.params === 'object' ? req.params : {}) as Record<
    string,
    unknown
  >;
  const latencyMs = 40 + Math.floor(Math.random() * 80);

  if (!method) {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        error: { code: -32600, message: 'Invalid Request: method required' },
      },
    };
  }

  // Method-specific happy paths
  if (method === 'session.create') {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        result: {
          session_id: 'sess_01MOCKDEMO000000000000',
          workspace_id: 'ws_01MOCKDEMO000000000000',
          profile: String(params.profile || 'safe-edit'),
          workspace_root: String(params.workspace_root || '/repo'),
        },
      },
    };
  }

  if (method === 'session.get' || method === 'session.list') {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        result:
          method === 'session.list'
            ? {
                sessions: [
                  {
                    session_id: 'sess_01MOCKDEMO000000000000',
                    profile: 'safe-edit',
                    state: 'active',
                  },
                ],
              }
            : {
                session_id: String(params.session_id || 'sess_01MOCKDEMO000000000000'),
                profile: 'safe-edit',
                state: 'active',
              },
      },
    };
  }

  if (method === 'task.submit') {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        result: {
          task_id: 'task_01MOCKDEMO000000000000',
          session_id: String(params.session_id || 'sess_01MOCKDEMO000000000000'),
          state: 'queued',
        },
      },
    };
  }

  if (method === 'daemon.status' || method === 'runtime.capabilities') {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        result: {
          ok: true,
          version: '0.6.x-mock',
          mode: 'docs-playground',
          note: 'Simulated response — not a live daemon.',
        },
      },
    };
  }

  if (method.startsWith('gateway.')) {
    return {
      status: 200,
      latencyMs,
      body: {
        jsonrpc: '2.0',
        id,
        result: {
          role: 'docs-mock',
          methods: [method],
          features: ['playground'],
        },
      },
    };
  }

  // Generic success envelope for any other method
  return {
    status: 200,
    latencyMs,
    body: {
      jsonrpc: '2.0',
      id,
      result: {
        ok: true,
        method,
        echo_params: params,
        note: 'Mock playground response. Switch to Live to hit a real endpoint.',
      },
    },
  };
}
