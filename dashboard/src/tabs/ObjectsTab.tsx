import { ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowDownToLine, ChevronRight, Database, File, Folder, History, Layers3, Plus,
  RotateCcw, Search, Trash2, Upload, X,
} from 'lucide-react';
import { api, ApiError, downloadObject, formatBytes, formatTime, getToken } from '../lib/api';
import type { Bucket, ObjectRow, Version } from '../types';
import {
  Button, EmptyState, Field, IconButton, Modal, Spinner, StatusTag,
} from '../components/ui';
import { useToast } from '../components/toast';

export default function ObjectsTab({ onUnauthorized }: { onUnauthorized: () => void }) {
  const toast = useToast();
  const [buckets, setBuckets] = useState<Bucket[]>([]);
  const [bucket, setBucket] = useState('');
  const [objects, setObjects] = useState<ObjectRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [objectsLoading, setObjectsLoading] = useState(false);
  const [filter, setFilter] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [versionFor, setVersionFor] = useState<ObjectRow | null>(null);
  const [deleteFor, setDeleteFor] = useState<ObjectRow | null>(null);

  const guard = (err: unknown) => {
    if (err instanceof ApiError && err.status === 401) {
      onUnauthorized();
      return true;
    }
    toast(err instanceof Error ? err.message : 'Request failed', 'error');
    return true;
  };

  const loadBuckets = async () => {
    try {
      const res = await api<{ buckets: Bucket[] }>('/buckets');
      setBuckets(res.buckets);
      setBucket((current) => current || res.buckets[0]?.name || '');
    } catch (err) {
      guard(err);
    } finally {
      setLoading(false);
    }
  };

  const loadObjects = async (name: string) => {
    if (!name) {
      setObjects([]);
      return;
    }
    setObjectsLoading(true);
    try {
      const res = await api<{ objects: ObjectRow[] }>(`/buckets/${encodeURIComponent(name)}/objects?limit=1000`);
      setObjects(res.objects);
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 404)) guard(err);
      setObjects([]);
    } finally {
      setObjectsLoading(false);
    }
  };

  useEffect(() => {
    void loadBuckets();
  }, []);

  useEffect(() => {
    void loadObjects(bucket);
  }, [bucket]);

  const current = buckets.find((b) => b.name === bucket);
  const filtered = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    if (!needle) return objects;
    return objects.filter((o) => o.key.toLowerCase().includes(needle) || o.content_type.toLowerCase().includes(needle));
  }, [objects, filter]);
  const totalBytes = objects.reduce((sum, o) => sum + o.size_bytes, 0);

  const createBucket = async (name: string) => {
    try {
      await api('/buckets', { method: 'POST', body: JSON.stringify({ name }) });
      toast(`Bucket “${name}” created.`, 'success');
      setCreateOpen(false);
      await loadBuckets();
      setBucket(name);
    } catch (err) {
      guard(err);
    }
  };

  const upload = async (file: File, key: string) => {
    if (!bucket) return;
    if (file.size > 16 << 20) {
      toast('Single PUT is capped at 16 MiB. Use multipart for larger files.', 'error');
      throw new Error('file too large');
    }
    try {
      const res = await fetch(`/api/buckets/${encodeURIComponent(bucket)}/objects/${key.split('/').map(encodeURIComponent).join('/')}`, {
        method: 'PUT',
        headers: {
          Authorization: `Bearer ${getToken()}`,
          'Content-Type': file.type || 'application/octet-stream',
          'Idempotency-Key': `upload-${crypto.randomUUID()}`,
        },
        body: file,
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new ApiError(res.status, body.error || res.statusText);
      }
      toast(`Uploaded ${key}.`, 'success');
      setUploadOpen(false);
      await loadObjects(bucket);
    } catch (err) {
      guard(err);
      throw err;
    }
  };

  const remove = async (row: ObjectRow) => {
    try {
      await api(`/buckets/${encodeURIComponent(bucket)}/objects/${row.key.split('/').map(encodeURIComponent).join('/')}`, { method: 'DELETE' });
      toast('Object deleted. Chunk copies are collected by the WAL-logged GC sweep.', 'success');
      setDeleteFor(null);
      await loadObjects(bucket);
    } catch (err) {
      guard(err);
    }
  };

  const download = async (row: ObjectRow) => {
    try {
      const blob = await downloadObject(bucket, row.key);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = row.key.split('/').pop() || row.key;
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      guard(err);
    }
  };

  if (loading) return <Spinner label="Loading buckets…" />;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="display text-[28px] leading-9 text-body">Object browser</h1>
          <p className="mt-1 max-w-2xl text-[13px] text-subtle">
            Immutable versions, verified chunk reads and deterministic multipart IDs. Objects live in buckets owned by your account.
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="secondary" onClick={() => setCreateOpen(true)}>
            <Plus size={15} /> New bucket
          </Button>
          <Button onClick={() => setUploadOpen(true)} disabled={!bucket}>
            <Upload size={15} /> Upload
          </Button>
        </div>
      </div>

      <div className="grid gap-5 lg:grid-cols-[268px_minmax(0,1fr)]">
        <aside className="space-y-3">
          <div className="card p-3">
            <div className="px-1 pb-2 text-[11px] font-semibold uppercase tracking-[0.1em] text-faint">Buckets</div>
            {buckets.length === 0 ? (
              <div className="rounded-lg border border-dashed border-edge px-3 py-6 text-center text-[12px] text-subtle">
                No buckets yet.
                <button className="mt-2 block w-full font-semibold text-primary hover:underline" onClick={() => setCreateOpen(true)}>
                  Create your first bucket
                </button>
              </div>
            ) : (
              <div className="space-y-1">
                {buckets.map((b) => {
                  const active = b.name === bucket;
                  return (
                    <button
                      key={b.name}
                      onClick={() => setBucket(b.name)}
                      className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-[13px] transition-colors ${
                        active ? 'bg-primary-soft font-semibold text-primary' : 'text-subtle hover:bg-muted hover:text-body'
                      }`}
                    >
                      <Folder size={15} className={active ? 'text-primary' : 'text-faint'} />
                      <span className="min-w-0 flex-1 truncate">{b.name}</span>
                      {active ? <ChevronRight size={13} /> : null}
                    </button>
                  );
                })}
              </div>
            )}
          </div>

          <div className="card p-4">
            <div className="text-[11px] font-semibold uppercase tracking-[0.1em] text-faint">Bucket summary</div>
            <div className="mt-3 space-y-2.5">
              <Row label="Objects" value={<span className="tnum">{objects.length}</span>} />
              <Row label="Stored" value={<span className="tnum">{formatBytes(totalBytes)}</span>} />
              <Row label="Replication" value={<span className="tnum">N=3 · W=2</span>} />
              <Row label="Created" value={current ? formatTime(current.created_at) : '—'} />
            </div>
          </div>
        </aside>

        <section className="card min-w-0 overflow-hidden">
          <div className="flex flex-wrap items-center gap-3 border-b border-edge px-4 py-3">
            <div className="flex items-center gap-2 text-[14px] font-semibold text-body">
              <Database size={16} className="text-primary" />
              {bucket || 'Select a bucket'}
            </div>
            <div className="relative ml-auto w-full max-w-[260px]">
              <Search size={14} className="pointer-events-none absolute left-2.5 top-2.5 text-faint" />
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Filter keys…"
                className="h-9 w-full rounded-lg border border-edge bg-muted pl-8 pr-8 text-[13px] text-body placeholder:text-faint focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20"
              />
              {filter ? (
                <button className="absolute right-2 top-2.5 text-faint hover:text-body" onClick={() => setFilter('')} aria-label="Clear filter">
                  <X size={14} />
                </button>
              ) : null}
            </div>
            <IconButton label="Refresh" onClick={() => void loadObjects(bucket)}>
              <RotateCcw size={15} />
            </IconButton>
          </div>

          {objectsLoading ? <Spinner label="Reading verified metadata…" /> : filtered.length === 0 ? (
            <div className="p-4">
              <EmptyState
                title={objects.length === 0 ? 'This bucket is empty' : 'No matching keys'}
                body={objects.length === 0 ? 'Upload a file (up to 16 MiB through the single PUT path) or seed the demo dataset.' : 'Try a shorter filter, or clear it to see every object.'}
                action={objects.length === 0 && bucket ? <Button variant="secondary" onClick={() => setUploadOpen(true)}><Upload size={15} /> Upload an object</Button> : null}
              />
            </div>
          ) : (
            <div className="overflow-x-auto scroll-thin">
              <table className="w-full min-w-[720px] table-fixed text-left">
                <thead>
                  <tr className="border-b border-edge bg-muted/60 text-[11px] uppercase tracking-[0.08em] text-faint">
                    <th className="px-4 py-2.5 font-semibold">Key</th>
                    <th className="w-[104px] px-3 py-2.5 font-semibold">Integrity</th>
                    <th className="w-[72px] px-3 py-2.5 font-semibold">Size</th>
                    <th className="w-[118px] px-3 py-2.5 font-semibold">Type</th>
                    <th className="w-[108px] px-3 py-2.5 font-semibold">Updated</th>
                    <th className="w-[112px] px-4 py-2.5 text-right font-semibold">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((row) => (
                    <tr key={row.key} className="group border-b border-edge/70 last:border-0 hover:bg-muted/50">
                      <td className="max-w-[320px] px-4 py-2.5">
                        <div className="flex items-center gap-2">
                          <File size={14} className="shrink-0 text-faint" />
                          <span className="truncate font-medium text-body" title={row.key}>{row.key}</span>
                        </div>
                        <div className="mt-0.5 truncate font-mono text-[11px] text-faint" title={row.etag}>{row.etag.slice(0, 20)}…</div>
                      </td>
                      <td className="px-3 py-2.5"><StatusTag tone="verified" /></td>
                      <td className="tnum whitespace-nowrap px-3 py-2.5 text-subtle">{formatBytes(row.size_bytes)}</td>
                      <td className="max-w-[150px] truncate px-3 py-2.5 text-[12px] text-subtle">{row.content_type || 'application/octet-stream'}</td>
                      <td className="whitespace-nowrap px-3 py-2.5 text-[12px] text-subtle">{formatTime(row.created_at)}</td>
                      <td className="px-4 py-2.5">
                        <div className="flex justify-end gap-0.5 opacity-60 transition-opacity group-hover:opacity-100">
                          <IconButton label="Download" onClick={() => void download(row)}><ArrowDownToLine size={15} /></IconButton>
                          <IconButton label="Version history" onClick={() => setVersionFor(row)}><History size={15} /></IconButton>
                          <IconButton label="Delete" onClick={() => setDeleteFor(row)} className="hover:text-danger"><Trash2 size={15} /></IconButton>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="flex items-center justify-between border-t border-edge px-4 py-2.5 text-[12px] text-faint">
            <span>{filtered.length} of {objects.length} objects</span>
            <span className="hidden items-center gap-1.5 sm:flex"><Layers3 size={12} /> Reads verify SHA-256 before bytes leave the API</span>
          </div>
        </section>
      </div>

      <CreateBucketModal open={createOpen} onClose={() => setCreateOpen(false)} onCreate={createBucket} />
      <UploadModal open={uploadOpen} onClose={() => setUploadOpen(false)} bucket={bucket} onUpload={upload} />
      <VersionsModal row={versionFor} bucket={bucket} onClose={() => setVersionFor(null)} onRollback={async (versionId) => {
        if (!versionFor) return;
        try {
          await api(`/buckets/${encodeURIComponent(bucket)}/rollback`, {
            method: 'POST',
            body: JSON.stringify({ key: versionFor.key, version_id: versionId }),
          });
          toast('Rolled back. The current pointer moved; no bytes were copied.', 'success');
          setVersionFor(null);
          await loadObjects(bucket);
        } catch (err) {
          guard(err);
        }
      }} />
      <Modal open={Boolean(deleteFor)} onClose={() => setDeleteFor(null)} title="Delete object?" subtitle={deleteFor?.key}>
        <p className="text-[13px] leading-6 text-subtle">
          A delete marker becomes the current version. Old versions stay readable until garbage collection, and chunk copies are removed through the WAL.
        </p>
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setDeleteFor(null)}>Cancel</Button>
          <Button variant="danger" onClick={() => deleteFor && void remove(deleteFor)}>Delete</Button>
        </div>
      </Modal>
    </div>
  );
}

function Row({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 text-[12px]">
      <span className="text-faint">{label}</span>
      <span className="font-medium text-body">{value}</span>
    </div>
  );
}

function CreateBucketModal({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (name: string) => Promise<void> }) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setName('');
  }, [open]);
  const submit = async () => {
    if (!name.trim()) return;
    setBusy(true);
    await onCreate(name.trim());
    setBusy(false);
  };
  return (
    <Modal open={open} onClose={onClose} title="New bucket" subtitle="Buckets are owner-scoped and checked on every operation.">
      <Field label="Bucket name" value={name} onChange={(e) => setName(e.target.value)} placeholder="documents" autoFocus />
      <p className="mt-2 text-[12px] text-faint">Lowercase letters, numbers, dots and hyphens work best.</p>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button loading={busy} onClick={() => void submit()} disabled={!name.trim()}>Create bucket</Button>
      </div>
    </Modal>
  );
}

function UploadModal({ open, onClose, bucket, onUpload }: {
  open: boolean;
  onClose: () => void;
  bucket: string;
  onUpload: (file: File, key: string) => Promise<void>;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [dragging, setDragging] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setFile(null);
      setKey('');
    }
  }, [open]);

  const choose = (next: File) => {
    setFile(next);
    setKey(next.webkitRelativePath || next.name);
  };

  const submit = async () => {
    if (!file || !key.trim()) return;
    setBusy(true);
    try {
      await onUpload(file, key.trim().replace(/^\/+/, ''));
    } catch {
      // the caller already surfaced the error; keep the modal open
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Upload object" subtitle={`Target bucket: ${bucket || 'none'}`}>
      <div
        onDragOver={(e) => { e.preventDefault(); setDragging(true); }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          const dropped = e.dataTransfer.files[0];
          if (dropped) choose(dropped);
        }}
        className={`flex cursor-pointer flex-col items-center justify-center rounded-xl border border-dashed px-4 py-8 text-center transition-colors ${
          dragging ? 'border-primary bg-primary-soft' : 'border-edge bg-muted hover:border-primary/50'
        }`}
        onClick={() => input.current?.click()}
      >
        <Upload size={22} className="text-primary" />
        <div className="mt-2 text-[13px] font-semibold text-body">{file ? file.name : 'Drop a file, or click to browse'}</div>
        <div className="mt-0.5 text-[12px] text-faint">{file ? formatBytes(file.size) : 'Single PUT up to 16 MiB'}</div>
      </div>
      <input
        ref={input}
        type="file"
        className="hidden"
        onChange={(e) => {
          const next = e.target.files?.[0];
          if (next) choose(next);
        }}
      />
      <div className="mt-4">
        <Field label="Object key" value={key} onChange={(e) => setKey(e.target.value)} placeholder="reports/2026/summary.txt" />
      </div>
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button loading={busy} onClick={() => void submit()} disabled={!file || !key.trim()}>
          <Upload size={15} /> Upload
        </Button>
      </div>
    </Modal>
  );
}

function VersionsModal({ row, bucket, onClose, onRollback }: {
  row: ObjectRow | null;
  bucket: string;
  onClose: () => void;
  onRollback: (versionId: string) => Promise<void>;
}) {
  const [versions, setVersions] = useState<Version[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');

  useEffect(() => {
    if (!row) return;
    setLoading(true);
    setError('');
    api<{ versions: Version[] }>(`/buckets/${encodeURIComponent(bucket)}/versions?key=${encodeURIComponent(row.key)}`)
      .then((res) => setVersions(res.versions))
      .catch((err) => setError(err instanceof Error ? err.message : 'Could not load versions'))
      .finally(() => setLoading(false));
  }, [row, bucket]);

  return (
    <Modal open={Boolean(row)} onClose={onClose} title="Version history" subtitle={row?.key} wide>
      {loading ? <Spinner label="Loading versions…" /> : error ? (
        <div className="rounded-lg border border-danger/30 bg-danger/5 p-3 text-[13px] text-danger">{error}</div>
      ) : (
        <div className="max-h-[360px] space-y-2 overflow-y-auto scroll-thin pr-1">
          {versions.map((version, index) => (
            <div key={version.version_id} className={`rounded-xl border p-3 ${index === 0 ? 'border-primary/40 bg-primary-soft/50' : 'border-edge bg-muted'}`}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="tnum text-[13px] font-semibold text-body">v{version.version_no}</span>
                {index === 0 ? <StatusTag tone="match" /> : <StatusTag tone="closed" />}
                {version.is_delete_marker ? <StatusTag tone="hold" /> : <StatusTag tone="verified" />}
                <span className="ml-auto text-[12px] text-subtle">{formatTime(version.created_at)}</span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[11px] text-faint">
                <span className="truncate">{version.version_id}</span>
                <span className="tnum">{formatBytes(version.size_bytes)}</span>
                <span className="truncate">{version.checksum}</span>
              </div>
              {index !== 0 && !version.is_delete_marker ? (
                <div className="mt-2 flex justify-end">
                  <Button
                    variant="secondary"
                    loading={busy === version.version_id}
                    onClick={async () => {
                      setBusy(version.version_id);
                      await onRollback(version.version_id);
                      setBusy('');
                    }}
                  >
                    <RotateCcw size={13} /> Make current
                  </Button>
                </div>
              ) : null}
            </div>
          ))}
        </div>
      )}
      <div className="mt-4 flex items-center justify-between gap-3 border-t border-edge pt-3 text-[12px] text-faint">
        <span>Rollback moves the current pointer; chunk bytes are reused.</span>
        <Button variant="ghost" onClick={onClose}>Close</Button>
      </div>
    </Modal>
  );
}
