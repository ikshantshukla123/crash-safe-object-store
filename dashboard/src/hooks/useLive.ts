import { useEffect, useRef, useState } from 'react';
import { api, chaos, getDemoToken } from '../lib/api';
import type { ChaosStatus, Metrics } from '../types';

export function useMetrics() {
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [connected, setConnected] = useState(false);
  const retry = useRef(0);

  useEffect(() => {
    let socket: WebSocket | null = null;
    let timer: number | undefined;
    let disposed = false;

    const poll = async () => {
      try {
        const snapshot = await api<Metrics>('/stats');
        if (!disposed) setMetrics(snapshot);
      } catch {
        // API still starting
      }
    };

    const connect = () => {
      const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
      socket = new WebSocket(`${scheme}://${window.location.host}/ws/metrics`);
      socket.onopen = () => {
        retry.current = 0;
        setConnected(true);
      };
      socket.onmessage = (event) => {
        try {
          setMetrics(JSON.parse(event.data) as Metrics);
        } catch {
          // ignore malformed frame
        }
      };
      socket.onclose = () => {
        setConnected(false);
        if (disposed) return;
        retry.current += 1;
        const delay = Math.min(1000 * 2 ** retry.current, 10000);
        timer = window.setTimeout(connect, delay);
      };
      socket.onerror = () => socket?.close();
    };

    poll();
    connect();
    const poller = window.setInterval(poll, 5000);
    return () => {
      disposed = true;
      window.clearInterval(poller);
      if (timer) window.clearTimeout(timer);
      socket?.close();
    };
  }, []);

  return { metrics, connected };
}

export function useWorkerStats() {
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  useEffect(() => {
    let disposed = false;
    const poll = async () => {
      try {
        const res = await fetch('/worker/stats');
        if (!res.ok) return;
        const data = (await res.json()) as Metrics;
        if (!disposed) setMetrics(data);
      } catch {
        // worker not up yet
      }
    };
    poll();
    const timer = window.setInterval(poll, 4000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, []);
  return metrics;
}

export function useChaosStatus(enabled: boolean) {
  const [status, setStatus] = useState<ChaosStatus | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!enabled) {
      setStatus(null);
      setError('Demo controls need a demo token.');
      return;
    }
    let disposed = false;
    const poll = async () => {
      try {
        const data = await chaos<ChaosStatus>('/status', 'GET');
        if (!disposed) {
          setStatus(data);
          setError('');
        }
      } catch (err) {
        if (!disposed) setError(err instanceof Error ? err.message : 'controller offline');
      }
    };
    poll();
    const timer = window.setInterval(poll, 4000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [enabled]);

  return { status, error };
}

export function useLogs(enabled: boolean) {
  const [lines, setLines] = useState<string[]>([]);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    if (!enabled) {
      setLines([]);
      return;
    }
    const token = encodeURIComponent(getDemoToken());
    const source = new EventSource(`/chaos/logs?token=${token}`);
    source.onopen = () => setConnected(true);
    source.onerror = () => setConnected(false);
    source.onmessage = (event) => {
      try {
        const line = JSON.parse(event.data) as string;
        setLines((current) => [...current.slice(-299), line]);
      } catch {
        setLines((current) => [...current.slice(-299), event.data]);
      }
    };
    return () => source.close();
  }, [enabled]);

  return { lines, connected };
}

export function useHealth() {
  const [ok, setOk] = useState<boolean | null>(null);
  useEffect(() => {
    let disposed = false;
    const check = async () => {
      try {
        const res = await fetch('/api/health');
        if (!disposed) setOk(res.ok);
      } catch {
        if (!disposed) setOk(false);
      }
    };
    check();
    const timer = window.setInterval(check, 8000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, []);
  return ok;
}
