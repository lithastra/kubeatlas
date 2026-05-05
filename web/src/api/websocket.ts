import type { QueryClient } from '@tanstack/react-query';

import type { Level } from './types';

// Message types — keep in sync with pkg/api.MessageType (Go side).
type MessageType = 'SUBSCRIBE' | 'UNSUBSCRIBE' | 'GRAPH_UPDATE' | 'PING' | 'PONG';

interface Envelope {
  type: MessageType;
  level?: Level;
  namespace?: string;
  kind?: string;
  name?: string;
  patch?: unknown;
  revision?: number;
}

export interface SubscribeFilter {
  level: Level;
  namespace?: string;
  kind?: string;
  name?: string;
}

export interface WatchClientOptions {
  url?: string;
  queryClient: QueryClient;
  // Heartbeat period in ms (default 30 000). Tests override.
  heartbeatMs?: number;
  // Initial reconnect backoff in ms (default 1 000). Doubles each
  // failure up to maxBackoffMs.
  initialBackoffMs?: number;
  maxBackoffMs?: number;
}

const defaultURL = (() => {
  if (typeof window === 'undefined') return '';
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/v1alpha1/watch`;
})();

// WatchClient is a single long-lived WebSocket the app boots in
// main.tsx. On every GRAPH_UPDATE it invalidates the relevant React
// Query cache key — Phase 1 v0.1.0 deliberately uses invalidate
// (next active hook refetches) rather than patch-merge, since the
// server doesn't yet emit precise patches and the aggregator queries
// resolve in single-digit milliseconds (see test/baseline). Patch
// merge is a Phase 2 (v1.0) optimisation.
//
// Reconnect uses exponential backoff with a 30s ceiling. The hub
// drops missed updates on reconnect rather than replaying via
// since_revision — spec calls this out as acceptable for v0.1.0.
export class WatchClient {
  private opts: Required<Omit<WatchClientOptions, 'url'>> & { url: string };
  private socket: WebSocket | null = null;
  private filter: SubscribeFilter = { level: 'cluster' };
  private heartbeat: ReturnType<typeof setInterval> | null = null;
  private reconnect: ReturnType<typeof setTimeout> | null = null;
  private backoff: number;
  private closed = false;

  constructor(opts: WatchClientOptions) {
    this.opts = {
      url: opts.url ?? defaultURL,
      queryClient: opts.queryClient,
      heartbeatMs: opts.heartbeatMs ?? 30_000,
      initialBackoffMs: opts.initialBackoffMs ?? 1_000,
      maxBackoffMs: opts.maxBackoffMs ?? 30_000,
    };
    this.backoff = this.opts.initialBackoffMs;
  }

  start(): void {
    this.closed = false;
    this.connect();
  }

  setFilter(filter: SubscribeFilter): void {
    this.filter = filter;
    if (this.socket && this.socket.readyState === WebSocket.OPEN) {
      this.send({ type: 'SUBSCRIBE', ...filter });
    }
  }

  close(): void {
    this.closed = true;
    if (this.heartbeat) clearInterval(this.heartbeat);
    if (this.reconnect) clearTimeout(this.reconnect);
    this.socket?.close();
  }

  private connect(): void {
    if (this.closed) return;
    try {
      this.socket = new WebSocket(this.opts.url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.socket.addEventListener('open', this.handleOpen);
    this.socket.addEventListener('message', this.handleMessage);
    this.socket.addEventListener('close', this.handleClose);
    this.socket.addEventListener('error', this.handleError);
  }

  private handleOpen = () => {
    this.backoff = this.opts.initialBackoffMs;
    this.send({ type: 'SUBSCRIBE', ...this.filter });
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = setInterval(() => {
      this.send({ type: 'PING' });
    }, this.opts.heartbeatMs);
  };

  private handleMessage = (ev: MessageEvent<string>) => {
    let env: Envelope;
    try {
      env = JSON.parse(ev.data) as Envelope;
    } catch {
      return;
    }
    if (env.type === 'GRAPH_UPDATE') {
      // Invalidate every active /graph and /resource query so the
      // hooks that are mounted refetch. React Query is smart about
      // batching the refetches.
      void this.opts.queryClient.invalidateQueries({ queryKey: ['graph'] });
      void this.opts.queryClient.invalidateQueries({ queryKey: ['resource'] });
    }
    // PING / PONG: no client-side action required (server initiates
    // its own heartbeat ticker; we send PINGs too so the connection
    // stays warm through corporate proxies).
  };

  private handleClose = () => {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.scheduleReconnect();
  };

  private handleError = () => {
    // Triggered before close; close handler does the reconnect.
  };

  private scheduleReconnect(): void {
    if (this.closed) return;
    if (this.reconnect) clearTimeout(this.reconnect);
    const wait = this.backoff;
    this.backoff = Math.min(this.backoff * 2, this.opts.maxBackoffMs);
    this.reconnect = setTimeout(() => this.connect(), wait);
  }

  private send(env: Envelope): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(env));
    }
  }
}
