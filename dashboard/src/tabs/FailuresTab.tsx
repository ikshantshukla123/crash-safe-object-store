import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Activity, AlertTriangle, CheckCircle2, Clock3, Flame, HeartPulse, Play, RotateCcw,
  Server, ShieldAlert, Skull, Terminal, Timer, Zap,
} from 'lucide-react';
import { chaos, getDemoToken } from '../lib/api';
import { useChaosStatus, useLogs, useMetrics, useWorkerStats } from '../hooks/useLive';
import { Button, EmptyState, Spinner, Stat, StatusTag } from '../components/ui';
import { useToast } from '../components/toast';

export default function FailuresTab({ onNeedDemo }: { onNeedDemo: () => void }) {
  const toast = useToast();
  const hasToken = Boolean(getDemoToken());
  const { status, error } = useChaosStatus(hasToken);
  const { lines, connected } = useLogs(hasToken);
  const { metrics, connected: wsConnected } = useMetrics();
  const worker = useWorkerStats();
  const [busy, setBusy] = useState('');
  const [k6, setK6] = useState<{ status: string; output: string; at: string } | null>(null);
  const [paused, setPaused] = useState(false);
  const [visibleLines, setVisibleLines] = useState<string[]>([]);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!paused) setVisibleLines(lines);
  }, [lines, paused]);

  useEffect(() => {
    const el = logRef.current;
    if (el && !paused) el.scrollTop = el.scrollHeight;
  }, [visibleLines, paused]);

  useEffect(() => {
    if (!hasToken) return;
    let disposed = false;
    const poll = async () => {
      try {
        const res = await chaos<{ status: string; output: string; at: string }>('/k6', 'GET');
        if (!disposed) setK6(res);
      } catch {
        // ignore
      }
    };
    poll();
    const timer = window.setInterval(poll, 4000);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [hasToken]);

  const containers = useMemo(() => Object.keys(status || {}).sort(), [status]);
  const nodes = containers.filter((name) => name.includes('node'));
  const apiName = containers.find((name) => name.includes('api')) || 'objstore-api';
  const upCount = nodes.filter((name) => status?.[name]?.running).length;
  const stats = worker || metrics;

  const act = async (label: string, path: string) => {
    setBusy(path);
    try {
      await chaos(path);
      toast(`${label} requested. Watching the logs for recovery.`, 'success');
    } catch (err) {
      toast(err instanceof Error ? err.message : `${label} failed`, 'error');
    } finally {
      setBusy('');
    }
  };

  if (!hasToken) {
    return (
      <div className="space-y-5">
        <Header upCount={null} />
        <div className="card p-4">
          <EmptyState
            title="Failure lab is locked"
            body="The chaos controller needs the demo token (and DEMO_MODE=true). Add it once, then kill nodes and watch the replicator repair them."
            action={<Button onClick={onNeedDemo}><Zap size={15} /> Add demo token</Button>}
          />
        </div>
      </div>
    );
  }

  if (!status && !error) return <Spinner label="Contacting chaos controller…" />;

  return (
    <div className="space-y-5">
      <Header upCount={upCount} />

      {error ? (
        <div className="flex items-start gap-2 rounded-xl border border-warning/40 bg-warning/10 p-3 text-[13px] text-warning-text">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <div>
            <div className="font-semibold">Chaos controller unavailable</div>
            <div className="text-[12px] opacity-80">{error} — check DEMO_MODE and that the chaos container is running.</div>
          </div>
        </div>
      ) : null}

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Objects recovered" value={stats?.objects_recovered ?? '—'} note="replicas healed by the repair loop" icon={<HeartPulse size={16} />} tone="primary" />
        <Stat label="Repair time" value={stats ? `${stats.repair_latency_ms} ms` : '—'} note={stats?.last_repair_unix ? `last repair ${new Date(stats.last_repair_unix * 1000).toLocaleTimeString()}` : 'waiting for a repair'} icon={<Timer size={16} />} tone="accent" />
        <Stat label="Repairs" value={stats?.replica_repairs ?? '—'} note="copies verified and marked healthy" icon={<RotateCcw size={16} />} />
        <Stat label="Data-loss alerts" value={stats?.data_loss_alerts ?? '—'} note={stats?.data_loss_alerts ? 'no healthy source remained' : 'none — every read had a source'} icon={<ShieldAlert size={16} />} tone={stats?.data_loss_alerts ? 'danger' : 'default'} />
      </div>

      <div className="grid gap-5 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,0.85fr)]">
        <section className="card overflow-hidden">
          <div className="flex flex-wrap items-center gap-2 border-b border-edge px-4 py-3">
            <Server size={16} className="text-primary" />
            <h2 className="text-[14px] font-semibold text-body">Cluster</h2>
            <span className="text-[12px] text-faint">{upCount}/{nodes.length || 3} storage nodes up</span>
            <div className="ml-auto flex items-center gap-1.5">
              <span className={`h-2 w-2 rounded-full ${wsConnected ? 'bg-success' : 'bg-warning'} ${wsConnected ? '' : 'pulse-soft'}`} />
              <span className="text-[11px] text-faint">{wsConnected ? 'live metrics' : 'polling metrics'}</span>
            </div>
          </div>
          <div className="grid gap-3 p-4 sm:grid-cols-2">
            {nodes.map((name, index) => {
              const node = status?.[name];
              const running = Boolean(node?.running);
              const short = name.replace('objstore-', '');
              return (
                <div key={name} className={`rounded-xl border p-3.5 ${running ? 'border-edge bg-surface' : 'border-danger/30 bg-danger/5'}`}>
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${running ? 'bg-primary-soft text-primary' : 'bg-danger/10 text-danger'}`}>
                        <Server size={15} />
                      </div>
                      <div>
                        <div className="text-[13px] font-semibold text-body">{short}</div>
                        <div className="text-[11px] text-faint">storage node {index + 1}</div>
                      </div>
                    </div>
                    {running ? <StatusTag tone="match" /> : <StatusTag tone="danger" />}
                  </div>
                  <div className="mt-3 flex items-center justify-between gap-2">
                    <span className="text-[11px] text-faint">{running ? 'healthy · serving verified reads' : 'down · replicas pending repair'}</span>
                    {running ? (
                      <Button variant="danger" className="h-8 px-2.5 text-[12px]" loading={busy === `/chaos/kill/${short}`} onClick={() => void act(`Kill ${short}`, `/chaos/kill/${short}`)}>
                        <Skull size={13} /> Kill
                      </Button>
                    ) : (
                      <Button variant="secondary" className="h-8 px-2.5 text-[12px]" loading={busy === `/chaos/heal/${short}`} onClick={() => void act(`Heal ${short}`, `/chaos/heal/${short}`)}>
                        <HeartPulse size={13} /> Heal
                      </Button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
          <div className="flex flex-wrap items-center gap-2 border-t border-edge bg-muted/40 px-4 py-3">
            <Button variant="danger" loading={busy === '/chaos/kill-server'} onClick={() => void act('Kill API', '/chaos/kill-server')}>
              <Flame size={15} /> Kill API server
            </Button>
            <span className="text-[11px] text-faint">SIGKILL, then Docker restarts it. The dashboard keeps streaming logs directly from chaos.</span>
            <div className="ml-auto flex items-center gap-1.5 text-[11px] text-faint">
              <CheckCircle2 size={12} className={status?.[apiName]?.running ? 'text-success' : 'text-danger'} />
              {apiName} {status?.[apiName]?.running ? 'running' : 'down'}
            </div>
          </div>
        </section>

        <section className="card overflow-hidden">
          <div className="flex items-center gap-2 border-b border-edge px-4 py-3">
            <Play size={16} className="text-primary" />
            <h2 className="text-[14px] font-semibold text-body">Load test</h2>
            <span className="ml-auto text-[11px] text-faint">k6 · 10 VUs · 20s</span>
          </div>
          <div className="p-4">
            <div className="flex items-center gap-2">
              <Button
                loading={busy === '/chaos/k6' || k6?.status === 'running'}
                disabled={k6?.status === 'running'}
                onClick={() => void act('k6 run', '/chaos/k6')}
              >
                <Play size={15} /> Run k6
              </Button>
              <span className="text-[12px] text-subtle">
                {k6 ? <span className="inline-flex items-center gap-1.5"><Activity size={12} /> {k6.status}</span> : 'idle'}
              </span>
            </div>
            <pre className="mt-3 max-h-[110px] overflow-auto rounded-lg bg-ink p-3 font-mono text-[11px] leading-5 text-[#A7ADB8] scroll-thin">
{k6?.output?.trim() || 'Runs GET list, GET object with checksum verification, and semantic search against the live API.'}
            </pre>
            <div className="mt-4 grid grid-cols-2 gap-2 text-[12px]">
              <MiniStat label="Cache hits" value={stats ? stats.cache_hits.toLocaleString() : '—'} />
              <MiniStat label="Cache misses" value={stats ? stats.cache_misses.toLocaleString() : '—'} />
              <MiniStat label="Object reads" value={stats ? stats.object_reads.toLocaleString() : '—'} />
              <MiniStat label="Embeddings" value={stats ? stats.embeddings_done.toLocaleString() : '—'} />
            </div>
          </div>
        </section>
      </div>

      <section className="overflow-hidden rounded-2xl border border-[#2A303A] bg-[#0A1220] shadow-card">
        <div className="flex flex-wrap items-center gap-2 border-b border-[#2A303A] px-4 py-3">
          <Terminal size={15} className="text-[#5FB79A]" />
          <h2 className="text-[13px] font-semibold text-[#EEF0F3]">Live logs</h2>
          <span className={`inline-flex items-center gap-1.5 rounded-full border border-[#2A303A] px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.08em] ${connected ? 'text-[#5FB79A]' : 'text-[#E0A54A]'}`}>
            <span className={`h-1.5 w-1.5 rounded-full ${connected ? 'bg-[#5FB79A]' : 'bg-[#E0A54A]'} ${connected ? '' : 'pulse-soft'}`} />
            {connected ? 'streaming' : 'reconnecting'}
          </span>
          <div className="ml-auto flex items-center gap-2">
            <button className="rounded px-2 py-1 text-[11px] font-medium text-[#8B919C] hover:bg-white/5 hover:text-[#EEF0F3]" onClick={() => setPaused(!paused)}>
              {paused ? 'Resume' : 'Pause'}
            </button>
            <button className="rounded px-2 py-1 text-[11px] font-medium text-[#8B919C] hover:bg-white/5 hover:text-[#EEF0F3]" onClick={() => setVisibleLines([])}>
              Clear
            </button>
          </div>
        </div>
        <div ref={logRef} className="h-[300px] overflow-y-auto px-4 py-3 font-mono text-[11px] leading-[1.7] text-[#C7CDD8] scroll-thin">
          {visibleLines.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-[#6E7480]">
              <Terminal size={22} />
              <span>Waiting for log lines. Kill a node to start the show.</span>
            </div>
          ) : (
            visibleLines.map((line, index) => (
              <div key={`${index}-${line.slice(0, 40)}`} className="whitespace-pre-wrap break-words">
                <span className="mr-2 select-none text-[#4A5364]">{String(index + 1).padStart(3, '0')}</span>
                {highlight(line)}
              </div>
            ))
          )}
        </div>
        <div className="flex items-center gap-3 border-t border-[#2A303A] px-4 py-2 text-[10px] uppercase tracking-[0.1em] text-[#6E7480]">
          <Clock3 size={11} /> repair {stats?.repair_latency_ms ?? 0} ms
          <span className="text-[#2A303A]">·</span> data loss {stats?.data_loss_alerts ?? 0}
          <span className="text-[#2A303A]">·</span> scrub failures {stats?.scrub_failures ?? 0}
        </div>
      </section>
    </div>
  );
}

function Header({ upCount }: { upCount: number | null }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="display text-[28px] leading-9 text-body">Failure lab</h1>
        <p className="mt-1 max-w-2xl text-[13px] text-subtle">
          Kill a storage node or the API, then watch quorum writes, verified reads and background repair bring the cluster back.
        </p>
      </div>
      <div className="flex items-center gap-2 rounded-full border border-edge bg-surface px-3 py-1.5 text-[12px] text-subtle">
        <HeartPulse size={13} className={upCount && upCount > 0 ? 'text-success' : 'text-danger'} />
        {upCount === null ? 'cluster unknown' : `${upCount}/3 nodes up`}
      </div>
    </div>
  );
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-edge bg-muted px-2.5 py-2">
      <div className="text-[10px] uppercase tracking-[0.08em] text-faint">{label}</div>
      <div className="tnum text-[14px] font-semibold text-body">{value}</div>
    </div>
  );
}

function highlight(line: string) {
  const upper = line.toUpperCase();
  if (upper.includes('ERROR') || upper.includes('FATAL') || upper.includes('DATA LOSS')) {
    return <span className="text-[#E07A62]">{line}</span>;
  }
  if (upper.includes('REPAIR') || upper.includes('HEALTHY') || upper.includes('REPLICAT')) {
    return <span className="text-[#5FB79A]">{line}</span>;
  }
  return line;
}
