const ENDPOINT = '/api/v1/command';
const ACTOR = 'user';
const REQUEST_TIMEOUT_MS = 15000;

export class CommandError extends Error {
  constructor(code, message, status) {
    super(message);
    this.name = 'CommandError';
    this.code = code;
    this.status = status;
  }
}

function requestSignal(signal) {
  if (typeof AbortSignal === 'undefined') return signal;
  const timeout =
    typeof AbortSignal.timeout === 'function' ? AbortSignal.timeout(REQUEST_TIMEOUT_MS) : null;
  if (signal && timeout && typeof AbortSignal.any === 'function') {
    return AbortSignal.any([signal, timeout]);
  }
  return signal || timeout;
}

export async function command(name, args = {}, options = {}) {
  let response;
  try {
    response = await fetch(ENDPOINT, {
      method: 'POST',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ command: name, actor: ACTOR, args }),
      signal: requestSignal(options.signal),
    });
  } catch (error) {
    if (options.signal?.aborted) {
      throw new DOMException('The request was cancelled.', 'AbortError');
    }
    if (error?.name === 'TimeoutError') {
      throw new CommandError('timeout', 'The Gilir API did not respond in time.');
    }
    if (error?.name === 'AbortError') throw error;
    throw new CommandError('network_error', 'Cannot reach the Gilir API.');
  }

  let payload;
  try {
    payload = await response.json();
  } catch {
    throw new CommandError(
      response.ok ? 'invalid_response' : `http_${response.status}`,
      response.ok
        ? 'The Gilir API returned an unreadable response.'
        : `Request failed (${response.status}).`,
      response.status,
    );
  }

  if (!response.ok || payload?.error) {
    const error = payload?.error;
    const code =
      typeof error?.code === 'string' && error.code
        ? error.code
        : `http_${response.status}`;
    const message =
      typeof error?.message === 'string' && error.message
        ? error.message
        : typeof error === 'string' && error
          ? error
          : `Request failed (${response.status}).`;
    throw new CommandError(code, message, response.status);
  }

  return payload?.data;
}
