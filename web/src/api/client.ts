// Tiny fetch wrapper. All API calls go through here so we have one
// place to add request-id headers, error normalisation, retries, etc.
// Keep it boring: no transformation, no interceptors, no
// authentication hooks — those land when the API needs them.

const defaultTimeoutMs = 15_000;

// ApiError is what every non-2xx response gets normalised into. The
// shape mirrors pkg/api.ErrorResponse from the Go side so callers can
// branch on .code without parsing twice.
export class ApiError extends Error {
  status: number;
  code: string;
  requestId?: string;

  constructor(status: number, code: string, message: string, requestId?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }
}

interface FetchOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
  headers?: Record<string, string>;
}

// fetchJSON runs a GET against url and parses the JSON body. Errors
// come back as typed ApiError instances; AbortError on timeout.
export async function fetchJSON<T>(url: string, opts: FetchOptions = {}): Promise<T> {
  const { signal: outerSignal, timeoutMs = defaultTimeoutMs, headers } = opts;

  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(new Error('request timeout')), timeoutMs);
  const linkAbort = () => controller.abort(outerSignal?.reason);
  if (outerSignal) outerSignal.addEventListener('abort', linkAbort, { once: true });

  try {
    const resp = await fetch(url, {
      method: 'GET',
      headers: { Accept: 'application/json', ...(headers ?? {}) },
      signal: controller.signal,
    });
    if (!resp.ok) {
      const requestId = resp.headers.get('x-request-id') ?? undefined;
      let code = 'unknown';
      let message = resp.statusText;
      try {
        const body = (await resp.json()) as { error?: string; code?: string };
        if (body?.code) code = body.code;
        if (body?.error) message = body.error;
      } catch {
        // body wasn't JSON — keep the statusText fallback
      }
      throw new ApiError(resp.status, code, message, requestId);
    }
    return (await resp.json()) as T;
  } finally {
    clearTimeout(timeoutId);
    if (outerSignal) outerSignal.removeEventListener('abort', linkAbort);
  }
}
