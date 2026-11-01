import { api } from './client';
import { message } from './utils';
export class Resource<T> {
	data = $state<T | null>(null);
	error = $state('');
	loading = $state(false);
	private token = 0;
	private request?: AbortController;
	private lifetime = new AbortController();
	get signal() {
		return this.lifetime.signal;
	}
	async load(path: string) {
		const token = ++this.token;
		this.request?.abort();
		this.request = new AbortController();
		this.loading = true;
		this.error = '';
		try {
			const result = await api<T>(path, { signal: AbortSignal.any([this.signal, this.request.signal]) });
			if (token === this.token) this.data = result;
		} catch (e) {
			if (token === this.token && !(e instanceof DOMException && e.name === 'AbortError')) this.error = message(e);
		} finally {
			if (token === this.token) this.loading = false;
		}
	}
	cancelLoad() {
		this.token++;
		this.request?.abort();
		this.loading = false;
	}
	destroy() {
		this.cancelLoad();
		this.lifetime.abort();
	}
}
