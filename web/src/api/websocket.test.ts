import { QueryClient } from '@tanstack/react-query';

import { WatchClient } from './websocket';

// Minimal in-process fake of the global WebSocket so tests don't
// need a real server. Behaves enough like the real WebSocket for
// WatchClient's needs: open / send / close / message dispatch.
class FakeSocket {
  static instances: FakeSocket[] = [];
  static OPEN = 1;
  readyState = 0;
  url: string;
  onopen?: () => void;
  onmessage?: (ev: { data: string }) => void;
  onclose?: () => void;
  onerror?: () => void;
  // addEventListener is the API WatchClient uses.
  private listeners: Record<string, Array<(ev: unknown) => void>> = {};

  sent: string[] = [];
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }

  addEventListener(name: string, cb: (ev: unknown) => void) {
    (this.listeners[name] ||= []).push(cb);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.closed = true;
    this.readyState = 3; // CLOSED
    this.dispatch('close', {});
  }

  // Test helpers.
  fireOpen() {
    this.readyState = FakeSocket.OPEN;
    this.dispatch('open', {});
  }
  fireMessage(payload: unknown) {
    this.dispatch('message', { data: JSON.stringify(payload) });
  }

  private dispatch(name: string, ev: unknown) {
    for (const cb of this.listeners[name] ?? []) cb(ev);
  }
}

// Install the fake on globalThis before each test. WatchClient
// references the global WebSocket constructor at connect() time.
beforeEach(() => {
  FakeSocket.instances.length = 0;
  (globalThis as unknown as { WebSocket: typeof FakeSocket }).WebSocket = FakeSocket;
  // Also expose the OPEN constant the way WatchClient checks it.
  (FakeSocket as unknown as { OPEN: number }).OPEN = 1;
});

describe('WatchClient', () => {
  it('subscribes immediately after the socket opens', () => {
    const qc = new QueryClient();
    const c = new WatchClient({
      queryClient: qc,
      url: 'ws://test/watch',
      heartbeatMs: 100_000,
    });
    c.start();
    const sock = FakeSocket.instances[0];
    sock.fireOpen();
    expect(sock.sent).toHaveLength(1);
    expect(JSON.parse(sock.sent[0])).toEqual({ type: 'SUBSCRIBE', level: 'cluster' });
    c.close();
  });

  it('changes filter on demand and resends SUBSCRIBE', () => {
    const qc = new QueryClient();
    const c = new WatchClient({
      queryClient: qc,
      url: 'ws://test/watch',
      heartbeatMs: 100_000,
    });
    c.start();
    const sock = FakeSocket.instances[0];
    sock.fireOpen();
    sock.sent.length = 0; // reset
    c.setFilter({ level: 'namespace', namespace: 'demo' });
    expect(JSON.parse(sock.sent[0])).toEqual({ type: 'SUBSCRIBE', level: 'namespace', namespace: 'demo' });
    c.close();
  });

  it('invalidates graph + resource queries on GRAPH_UPDATE', async () => {
    const qc = new QueryClient();
    const spy = jest.spyOn(qc, 'invalidateQueries');
    const c = new WatchClient({
      queryClient: qc,
      url: 'ws://test/watch',
      heartbeatMs: 100_000,
    });
    c.start();
    const sock = FakeSocket.instances[0];
    sock.fireOpen();
    sock.fireMessage({
      type: 'GRAPH_UPDATE',
      level: 'cluster',
      namespace: 'demo',
      kind: 'Pod',
      name: 'p',
      revision: 7,
    });
    expect(spy).toHaveBeenCalledWith({ queryKey: ['graph'] });
    expect(spy).toHaveBeenCalledWith({ queryKey: ['resource'] });
    c.close();
  });

  it('reconnects with backoff after a close', () => {
    jest.useFakeTimers();
    try {
      const qc = new QueryClient();
      const c = new WatchClient({
        queryClient: qc,
        url: 'ws://test/watch',
        heartbeatMs: 100_000,
        initialBackoffMs: 100,
        maxBackoffMs: 1_000,
      });
      c.start();
      const first = FakeSocket.instances[0];
      first.fireOpen();
      first.close();
      // Advance past the first backoff.
      jest.advanceTimersByTime(150);
      expect(FakeSocket.instances).toHaveLength(2);
      // Bring the second connection up + close again — backoff doubles.
      const second = FakeSocket.instances[1];
      second.fireOpen();
      second.close();
      jest.advanceTimersByTime(250);
      expect(FakeSocket.instances).toHaveLength(3);
      c.close();
    } finally {
      jest.useRealTimers();
    }
  });
});
