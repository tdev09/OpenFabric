import { writable } from 'svelte/store';

export interface WOLDevice {
  mac: string;
  name: string;
  broadcast_ip: string;
  last_ip: string;
  linked_node_id: string;
  last_woken: string;
  wake_count: number;
}

export interface DiscoveredDevice {
  ip: string;
  mac: string;
  interface: string;
  linked_node_id?: string;
}

// Stores
export const wolDevices = writable<WOLDevice[]>([]);
export const discoveredDevices = writable<DiscoveredDevice[]>([]);
export const scanning = writable(false);
export const wakingDevices = writable<Record<string, boolean>>({});

const BASE = '/api';

export async function loadWOLDevices() {
  try {
    const res = await fetch(`${BASE}/wol/devices`);
    if (!res.ok) throw new Error('Failed to load registered WoL devices');
    const data = await res.json();
    wolDevices.set(data ?? []);
  } catch (e) {
    console.error('loadWOLDevices error:', e);
  }
}

export async function registerWOLDevice(device: Partial<WOLDevice>) {
  const res = await fetch(`${BASE}/wol/devices`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(device)
  });
  if (!res.ok) {
    const err = await res.json();
    throw new Error(err.error || 'Failed to register device');
  }
  await loadWOLDevices();
}

export async function unregisterWOLDevice(mac: string) {
  const res = await fetch(`${BASE}/wol/devices/${encodeURIComponent(mac)}`, {
    method: 'DELETE'
  });
  if (!res.ok) {
    const err = await res.json();
    throw new Error(err.error || 'Failed to unregister device');
  }
  await loadWOLDevices();
}

export async function wakeWOLDevice(mac: string) {
  wakingDevices.update(curr => ({ ...curr, [mac]: true }));
  try {
    const res = await fetch(`${BASE}/wol/wake/${encodeURIComponent(mac)}`, {
      method: 'POST'
    });
    if (!res.ok) {
      const err = await res.json();
      throw new Error(err.error || 'Failed to send wake packet');
    }
    
    // Auto-update local state representation last_woken
    wolDevices.update(devs => devs.map(d => {
      if (d.mac.toLowerCase() === mac.toLowerCase()) {
        return {
          ...d,
          last_woken: new Date().toISOString(),
          wake_count: d.wake_count + 1
        };
      }
      return d;
    }));
  } finally {
    // Keep the "waking" animation active for 5 seconds (grace period)
    setTimeout(() => {
      wakingDevices.update(curr => {
        const copy = { ...curr };
        delete copy[mac];
        return copy;
      });
    }, 5000);
  }
}

export async function scanWOLNetwork() {
  scanning.set(true);
  discoveredDevices.set([]);
  try {
    const res = await fetch(`${BASE}/wol/scan`);
    if (!res.ok) throw new Error('Failed to run network sweep');
    const data = await res.json();
    discoveredDevices.set(data ?? []);
  } catch (e) {
    console.error('scanWOLNetwork error:', e);
    throw e;
  } finally {
    scanning.set(false);
  }
}
