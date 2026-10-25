export type Bucket = { name: string; created_at: string };

export type ObjectRow = {
  key: string;
  size_bytes: number;
  etag: string;
  content_type: string;
  created_at: string;
};

export type Version = {
  version_id: string;
  version_no: number;
  size_bytes: number;
  checksum: string;
  content_type: string;
  is_delete_marker: boolean;
  created_at: string;
};

export type SearchHit = {
  key: string;
  bucket: string;
  version_id: string;
  size_bytes: number;
  etag: string;
  content_type: string;
  score: number;
  tier: 'High Potential' | 'Good' | 'Worth Exploring' | 'Low';
};

export type Metrics = {
  object_writes: number;
  object_reads: number;
  cache_hits: number;
  cache_misses: number;
  replica_repairs: number;
  objects_recovered: number;
  repair_failures: number;
  scrub_failures: number;
  data_loss_alerts: number;
  embeddings_done: number;
  chaos_actions: number;
  repair_latency_ms: number;
  last_repair_unix: number;
  uptime_seconds: number;
};

export type ChaosStatus = Record<string, { running: boolean; error: string }>;
