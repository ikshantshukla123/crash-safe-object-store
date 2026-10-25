import { ReactNode, ButtonHTMLAttributes, InputHTMLAttributes, useEffect } from 'react';
import {
  AlertTriangle, CheckCircle2, Compass, Loader2, Sparkles, Star, ThumbsUp, X, XCircle,
  CalendarCheck, CircleSlash, FileSearch, Send, ShieldCheck,
} from 'lucide-react';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'quiet';

const variants: Record<Variant, string> = {
  primary:
    'bg-primary text-primary-fg border border-primary hover:bg-primary-hover hover:border-primary-hover shadow-sm',
  secondary:
    'bg-surface text-body border border-edge hover:bg-muted',
  ghost:
    'bg-transparent text-subtle border border-transparent hover:bg-muted hover:text-body',
  danger:
    'bg-surface text-danger border border-edge hover:bg-muted',
  quiet:
    'bg-muted text-body border border-edge hover:border-primary/40',
};

export function Button({
  variant = 'primary', loading, className = '', children, ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; loading?: boolean }) {
  return (
    <button
      {...props}
      disabled={props.disabled || loading}
      className={`inline-flex h-9 items-center justify-center gap-2 rounded-lg px-3.5 text-[13px] font-semibold transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${variants[variant]} ${className}`}
    >
      {loading ? <Loader2 size={15} className="animate-spin" /> : null}
      {children}
    </button>
  );
}

export function IconButton({
  label, className = '', children, ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      {...props}
      aria-label={label}
      title={label}
      className={`inline-flex h-9 w-9 items-center justify-center rounded-lg border border-transparent text-subtle transition-colors hover:bg-muted hover:text-body disabled:opacity-50 ${className}`}
    >
      {children}
    </button>
  );
}

export type StatusTone =
  | 'new' | 'verified' | 'shortlisting' | 'match' | 'meeting'
  | 'hold' | 'matched' | 'closed' | 'danger';

const statusMeta: Record<StatusTone, { label: string; icon: ReactNode }> = {
  new: { label: 'New', icon: <Sparkles size={12} /> },
  verified: { label: 'Verified', icon: <ShieldCheck size={12} /> },
  shortlisting: { label: 'Shortlisting', icon: <FileSearch size={12} /> },
  match: { label: 'Match Sent', icon: <Send size={12} /> },
  meeting: { label: 'Meeting Scheduled', icon: <CalendarCheck size={12} /> },
  hold: { label: 'On Hold', icon: <CircleSlash size={12} /> },
  matched: { label: 'Matched', icon: <CheckCircle2 size={12} /> },
  closed: { label: 'Closed', icon: <XCircle size={12} /> },
  danger: { label: 'Failed', icon: <AlertTriangle size={12} /> },
};

export function StatusTag({ tone }: { tone: StatusTone }) {
  const meta = statusMeta[tone];
  return (
    <span className={`status status-${tone}`}>
      {meta.icon}
      {meta.label}
    </span>
  );
}

export type Tier = 'High Potential' | 'Good' | 'Worth Exploring' | 'Low';

export function TierBadge({ tier }: { tier: Tier }) {
  if (tier === 'High Potential') {
    return (
      <span className="status tier-high">
        <Star size={12} fill="currentColor" />
        High Potential
      </span>
    );
  }
  if (tier === 'Good') {
    return (
      <span className="status tier-good">
        <ThumbsUp size={12} />
        Good
      </span>
    );
  }
  if (tier === 'Worth Exploring') {
    return (
      <span className="status tier-explore">
        <Compass size={12} />
        Worth Exploring
      </span>
    );
  }
  return null;
}

export function Field({ label, hint, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-[13px] font-medium text-body">{label}</span>
      <input
        {...props}
        className="h-9 w-full rounded-lg border border-edge bg-surface px-3 text-[13px] text-body placeholder:text-faint focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20"
      />
      {hint ? <span className="mt-1 block text-[12px] text-faint">{hint}</span> : null}
    </label>
  );
}

export function Modal({
  open, onClose, title, subtitle, children, wide = false,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  subtitle?: string;
  children: ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = '';
    };
  }, [open, onClose]);

  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-ink/45 backdrop-blur-[2px]" onClick={onClose} />
      <div className={`relative w-full ${wide ? 'max-w-2xl' : 'max-w-md'} card rise p-5`}>
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2 className="text-[18px] font-semibold leading-6 text-body">{title}</h2>
            {subtitle ? <p className="mt-1 text-[13px] text-subtle">{subtitle}</p> : null}
          </div>
          <IconButton label="Close" onClick={onClose}>
            <X size={17} />
          </IconButton>
        </div>
        {children}
      </div>
    </div>
  );
}

export function Stat({
  label, value, note, icon, tone = 'default',
}: {
  label: string;
  value: ReactNode;
  note?: string;
  icon?: ReactNode;
  tone?: 'default' | 'primary' | 'accent' | 'danger';
}) {
  const ring = {
    default: 'border-edge bg-surface',
    primary: 'border-primary/25 bg-primary-soft',
    accent: 'border-accent/30 bg-accent-soft',
    danger: 'border-danger/25 bg-surface',
  }[tone];
  return (
    <div className={`rounded-xl border p-4 ${ring}`}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-[12px] font-semibold uppercase tracking-[0.08em] text-subtle">{label}</span>
        <span className={tone === 'danger' ? 'text-danger' : tone === 'accent' ? 'text-accent-text' : 'text-primary'}>{icon}</span>
      </div>
      <div className="tnum mt-2 text-[24px] font-semibold leading-8 tracking-tight text-body">{value}</div>
      {note ? <div className="mt-0.5 text-[12px] text-faint">{note}</div> : null}
    </div>
  );
}

export function EmptyState({ title, body, action }: { title: string; body: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center rounded-xl border border-dashed border-edge bg-muted/60 px-6 py-12 text-center">
      <div className="mb-3 flex h-11 w-11 items-center justify-center rounded-full bg-primary-soft text-primary">
        <FileSearch size={20} />
      </div>
      <h3 className="text-[15px] font-semibold text-body">{title}</h3>
      <p className="mt-1 max-w-sm text-[13px] text-subtle">{body}</p>
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-subtle" role="status">
      <Loader2 size={16} className="animate-spin" />
      {label}
    </div>
  );
}
