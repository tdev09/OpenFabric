import { writable } from 'svelte/store';

export interface TunnelPeer {
	node_id: string;
	public_key: string;
	tunnel_ip: string;
	last_seen: string;
	latency_ms: number;
	bytes_rx: number;
	bytes_tx: number;
}

export interface TunnelStatus {
	state: 'disconnected' | 'connecting' | 'connected' | 'error';
	tunnel_id: string;
	assigned_ip: string;
	relay_url: string;
	public_key: string;
	peers: TunnelPeer[];
	pin_enabled: boolean;
	tunnel_url: string;
	error?: string;
}

export const tunnelStatus = writable<TunnelStatus>({
	state: 'disconnected',
	tunnel_id: '',
	assigned_ip: '',
	relay_url: '127.0.0.1:4893',
	public_key: '',
	peers: [],
	pin_enabled: false,
	tunnel_url: ''
});

const BASE = '/api/tunnel';

async function request<T>(path: string, options?: RequestInit): Promise<T> {
	const res = await fetch(BASE + path, options);
	if (!res.ok) {
		const errData = await res.json().catch(() => ({}));
		throw new Error(errData.error || `HTTP ${res.status}: ${res.statusText}`);
	}
	if (res.status === 204) return {} as T;
	return res.json();
}

export async function loadTunnelStatus() {
	try {
		const status = await request<TunnelStatus>('/status');
		tunnelStatus.set(status);
	} catch (e) {
		console.error('failed to load tunnel status', e);
	}
}

export async function enableTunnel() {
	tunnelStatus.update(s => ({ ...s, state: 'connecting' }));
	try {
		await request<any>('/enable', { method: 'POST' });
		await loadTunnelStatus();
	} catch (e: any) {
		tunnelStatus.update(s => ({ ...s, state: 'error', error: e.message }));
		throw e;
	}
}

export async function disableTunnel() {
	try {
		await request<any>('/disable', { method: 'POST' });
		tunnelStatus.update(s => ({
			...s,
			state: 'disconnected',
			tunnel_id: '',
			assigned_ip: '',
			peers: [],
			tunnel_url: ''
		}));
	} catch (e: any) {
		console.error('failed to disable tunnel', e);
		throw e;
	}
}

export async function generatePIN(): Promise<string> {
	try {
		const res = await request<{ pin: string }>('/pin/generate', { method: 'POST' });
		tunnelStatus.update(s => ({ ...s, pin_enabled: true }));
		return res.pin;
	} catch (e: any) {
		console.error('failed to generate PIN', e);
		throw e;
	}
}

export async function revokePIN() {
	try {
		await request<any>('/pin', { method: 'DELETE' });
		tunnelStatus.update(s => ({ ...s, pin_enabled: false }));
	} catch (e: any) {
		console.error('failed to revoke PIN', e);
		throw e;
	}
}

export async function updateRelay(url: string) {
	try {
		await request<any>('/relay', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ url })
		});
		tunnelStatus.update(s => ({ ...s, relay_url: url }));
	} catch (e: any) {
		console.error('failed to update relay url', e);
		throw e;
	}
}
