import { FormEvent, useMemo, useState } from 'react';
import {
  ArrowDownToLine, BrainCircuit, Compass, EyeOff, FileText, Search, Sparkles, Star, ThumbsUp,
} from 'lucide-react';
import { api, ApiError, downloadObject, formatBytes } from '../lib/api';
import type { SearchHit } from '../types';
import { Button, EmptyState, Spinner, StatusTag, TierBadge } from '../components/ui';
import { useToast } from '../components/toast';

const suggestions = [
  'quarterly revenue report',
  'customer churn analysis',
  'security audit tracker',
  'logistics routing study',
  'incident review',
];

export default function SearchTab({ onUnauthorized }: { onUnauthorized: () => void }) {
  const toast = useToast();
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchHit[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [showLow, setShowLow] = useState(false);
  const [lastQuery, setLastQuery] = useState('');

  const run = async (value: string) => {
    const trimmed = value.trim();
    if (!trimmed) return;
    setLoading(true);
    setLastQuery(trimmed);
    try {
      const res = await api<{ results: SearchHit[] }>(`/search?q=${encodeURIComponent(trimmed)}&limit=25`);
      setResults(res.results);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
        return;
      }
      toast(err instanceof Error ? err.message : 'Search failed', 'error');
      setResults([]);
    } finally {
      setLoading(false);
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    void run(query);
  };

  const visible = useMemo(() => (results || []).filter((hit) => showLow || hit.tier !== 'Low'), [results, showLow]);
  const lowCount = (results || []).filter((hit) => hit.tier === 'Low').length;
  const highs = visible.filter((hit) => hit.tier === 'High Potential').length;

  const download = async (hit: SearchHit) => {
    try {
      const blob = await downloadObject(hit.bucket, hit.key);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = hit.key.split('/').pop() || hit.key;
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Download failed', 'error');
    }
  };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="display text-[28px] leading-9 text-body">AI search</h1>
          <p className="mt-1 max-w-2xl text-[13px] text-subtle">
            pgvector cosine search over object embeddings. Results are tagged by confidence; low-signal matches stay hidden unless you ask for them.
          </p>
        </div>
        <div className="flex items-center gap-2 rounded-full border border-edge bg-surface px-3 py-1.5 text-[12px] text-subtle">
          <BrainCircuit size={13} className="text-primary" /> text-embedding-3-small · 1536d
        </div>
      </div>

      <section className="card overflow-hidden">
        <form onSubmit={submit} className="p-4">
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="relative flex-1">
              <Search size={17} className="pointer-events-none absolute left-3 top-3 text-faint" />
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Describe what you are looking for…"
                className="h-11 w-full rounded-xl border border-edge bg-muted pl-10 pr-3 text-[14px] text-body placeholder:text-faint focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20"
              />
            </div>
            <Button type="submit" loading={loading} className="h-11 px-5">
              <Sparkles size={16} /> Search
            </Button>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            <span className="mr-1 text-[11px] font-semibold uppercase tracking-[0.08em] text-faint">Try</span>
            {suggestions.map((suggestion) => (
              <button
                key={suggestion}
                type="button"
                onClick={() => { setQuery(suggestion); void run(suggestion); }}
                className="rounded-full border border-edge bg-surface px-2.5 py-1 text-[12px] text-subtle transition-colors hover:border-primary/40 hover:text-primary"
              >
                {suggestion}
              </button>
            ))}
          </div>
        </form>
      </section>

      {loading ? (
        <div className="card"><Spinner label="Embedding query and scanning pgvector…" /></div>
      ) : results === null ? (
        <div className="card p-4">
          <EmptyState
            title="Search your objects semantically"
            body="Try a phrase rather than an exact key. The worker embeds new versions in the background and the sweeper catches anything missed."
          />
        </div>
      ) : (
        <section className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-[14px] font-semibold text-body">
              {visible.length} result{visible.length === 1 ? '' : 's'} for “{lastQuery}”
            </h2>
            {highs > 0 ? <span className="status tier-high"><Star size={12} fill="currentColor" />{highs} high potential</span> : null}
            {lowCount > 0 ? (
              <button
                onClick={() => setShowLow(!showLow)}
                className="ml-auto inline-flex items-center gap-1.5 rounded-full border border-edge bg-surface px-2.5 py-1 text-[12px] text-subtle hover:text-body"
              >
                {showLow ? <EyeOff size={12} /> : <Compass size={12} />}
                {showLow ? 'Hide' : 'Show'} {lowCount} low-confidence
              </button>
            ) : null}
          </div>

          {visible.length === 0 ? (
            <div className="card p-4">
              <EmptyState
                title="No confident matches"
                body={lowCount > 0 ? 'Only low-confidence results came back. Show them to inspect the long tail.' : 'Try a broader phrase, or seed the demo dataset so there is something to search.'}
                action={lowCount > 0 ? <Button variant="secondary" onClick={() => setShowLow(true)}><Compass size={15} /> Show low-confidence</Button> : null}
              />
            </div>
          ) : (
            <div className="space-y-2">
              {visible.map((hit, index) => (
                <article key={`${hit.bucket}/${hit.key}/${hit.version_id}`} className="card group p-4 rise" style={{ animationDelay: `${Math.min(index * 35, 280)}ms` }}>
                  <div className="flex flex-wrap items-start gap-3">
                    <div className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-xl ${
                      hit.tier === 'High Potential' ? 'bg-accent-soft text-accent-text' : hit.tier === 'Good' ? 'bg-primary-soft text-success' : 'bg-muted text-subtle'
                    }`}>
                      {hit.tier === 'High Potential' ? <Star size={17} fill="currentColor" /> : hit.tier === 'Good' ? <ThumbsUp size={16} /> : <FileText size={16} />}
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="truncate text-[14px] font-semibold text-body">{hit.key}</h3>
                        <TierBadge tier={hit.tier} />
                        <StatusTag tone="verified" />
                      </div>
                      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-faint">
                        <span className="font-medium text-subtle">{hit.bucket}</span>
                        <span className="tnum">{formatBytes(hit.size_bytes)}</span>
                        <span>{hit.content_type || 'application/octet-stream'}</span>
                        <span className="font-mono">{hit.etag.slice(0, 18)}…</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <div className="text-right">
                        <div className="tnum text-[16px] font-semibold leading-5 text-body">{(hit.score * 100).toFixed(1)}%</div>
                        <div className="text-[10px] uppercase tracking-[0.08em] text-faint">cosine</div>
                      </div>
                      <Button variant="secondary" className="h-8 px-2.5 text-[12px]" onClick={() => void download(hit)}>
                        <ArrowDownToLine size={14} /> Download
                      </Button>
                    </div>
                  </div>
                  <div className="mt-3 h-1 overflow-hidden rounded-full bg-muted">
                    <div
                      className={`h-full rounded-full ${hit.tier === 'High Potential' ? 'bg-accent' : hit.tier === 'Good' ? 'bg-success' : 'bg-verified'}`}
                      style={{ width: `${Math.max(3, Math.min(100, hit.score * 100))}%` }}
                    />
                  </div>
                </article>
              ))}
            </div>
          )}
          <p className="flex items-center gap-1.5 text-[11px] text-faint">
            <BrainCircuit size={12} /> Embeddings are at-least-once; a sweeper re-enqueues versions with no vector after a Redis wipe.
          </p>
        </section>
      )}
    </div>
  );
}

