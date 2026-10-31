let csrf = '';
let sessionController = new AbortController();
let unauthorized = () => {};
export class APIError extends Error {
	constructor(
		message: string,
		public status: number,
	) {
		super(message);
	}
}
export function configureSession(token: string, onUnauthorized: () => void) {
	sessionController.abort();
	sessionController = new AbortController();
	csrf = token;
	unauthorized = onUnauthorized;
}
export function clearSession() {
	sessionController.abort();
	sessionController = new AbortController();
	csrf = '';
}
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
	const headers = new Headers(options.headers);
	if (options.method && !['GET', 'HEAD'].includes(options.method)) headers.set('X-CineRoute-CSRF', csrf);
	const response = await fetch('/api' + path, {
		...options,
		headers,
		credentials: 'same-origin',
		signal: options.signal ? AbortSignal.any([options.signal, sessionController.signal]) : sessionController.signal,
	});
	const data = await response.json();
	if (!response.ok) {
		if (response.status === 401 && path !== '/login') unauthorized();
		throw new APIError(data.error || `Request failed (${response.status})`, response.status);
	}
	return data as T;
}
export const post = <T>(path: string, body: unknown = {}) =>
	api<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
