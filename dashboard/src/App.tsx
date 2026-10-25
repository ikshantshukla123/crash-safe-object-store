import { FormEvent, ReactNode, useEffect, useState } from 'react';
import {
  Activity, Database, KeyRound, Layers, LogOut, Moon, Search, Sun, Wifi, WifiOff,
} from 'lucide-react';
import { api, ApiError, demoApi, getDemoToken, getToken, setDemoToken, setToken } from './lib/api';
import { useHealth } from './hooks/useLive';
import { Button, Field, IconButton, Modal } from './components/ui';
import { ToastProvider, useToast } from './components/toast';
import ObjectsTab from './tabs/ObjectsTab';
import FailuresTab from './tabs/FailuresTab';
import SearchTab from './tabs/SearchTab';

type Tab = 'objects' | 'failures' | 'search';

const emailKey = 'objstore.email';

const tabs: { id: Tab; label: string; icon: typeof Database }[] = [
  { id: 'objects', label: 'Objects', icon: Database },
  { id: 'failures', label: 'Failures', icon: Activity },
  { id: 'search', label: 'AI Search', icon: Search },
];

export default function App() {
  return (
    <ToastProvider>
      <Shell />
    </ToastProvider>
  );
}

function Shell() {
  const toast = useToast();
  const [token, setTokenState] = useState(getToken());
  const [email, setEmail] = useState(localStorage.getItem(emailKey) || '');
  const [checking, setChecking] = useState(Boolean(getToken()));
  const [tab, setTab] = useState<Tab>('objects');
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    const saved = localStorage.getItem('objstore.theme');
    if (saved === 'light' || saved === 'dark') return saved;
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  });
  const [demoOpen, setDemoOpen] = useState(false);
  const [demoValue, setDemoValue] = useState(getDemoToken());
  const [demoBusy, setDemoBusy] = useState<'reset' | 'seed' | ''>('');
  const health = useHealth();

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    localStorage.setItem('objstore.theme', theme);
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#101318' : '#F6F3EE');
  }, [theme]);

  useEffect(() => {
    if (!token) {
      setChecking(false);
      return;
    }
    let disposed = false;
    api('/buckets')
      .then(() => {
        if (!disposed) setChecking(false);
      })
      .catch((err) => {
        if (disposed) return;
        if (err instanceof ApiError && err.status === 401) {
          setToken('');
          setTokenState('');
        }
        setChecking(false);
      });
    return () => {
      disposed = true;
    };
  }, [token]);

  const signIn = (nextToken: string, nextEmail: string) => {
    setToken(nextToken);
    localStorage.setItem(emailKey, nextEmail);
    setTokenState(nextToken);
    setEmail(nextEmail);
  };

  const signOut = () => {
    setToken('');
    localStorage.removeItem(emailKey);
    setTokenState('');
    setEmail('');
  };

  const saveDemo = () => {
    setDemoToken(demoValue.trim());
    setDemoOpen(false);
    toast('Demo token saved.', 'success');
  };

  const runDemo = async (action: 'reset' | 'seed') => {
    if (!demoValue.trim() && !getDemoToken()) {
      toast('Enter the demo token first.', 'error');
      return;
    }
    setDemoBusy(action);
    setDemoToken(demoValue.trim());
    try {
      const res = await demoApi<{ token?: string; email?: string }>(`/admin/${action}`, demoValue.trim());
      if (res.token && res.email) {
        signIn(res.token, res.email);
        setTab('objects');
      }
      toast(action === 'reset' ? 'Demo data cleared.' : 'Seeded 100 objects and signed you in.', 'success');
      setDemoOpen(false);
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Demo action failed', 'error');
    } finally {
      setDemoBusy('');
    }
  };

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="flex items-center gap-3 text-subtle">
          <span className="h-2.5 w-2.5 rounded-full bg-primary pulse-soft" />
          Checking session…
        </div>
      </div>
    );
  }

  if (!token) {
    return (
      <AuthScreen
        onSignedIn={signIn}
        theme={theme}
        onToggleTheme={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
        openDemo={() => setDemoOpen(true)}
        demoModal={
          <Modal open={demoOpen} onClose={() => setDemoOpen(false)} title="Load the demo dataset" subtitle="Requires the demo token configured on the chaos controller and API.">
            <DemoForm
              value={demoValue}
              onChange={setDemoValue}
              busy={demoBusy}
              onReset={() => runDemo('reset')}
              onSeed={() => runDemo('seed')}
            />
          </Modal>
        }
      />
    );
  }

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-30 border-b border-edge bg-canvas/85 backdrop-blur-md">
        <div className="mx-auto flex w-full max-w-[1220px] items-center gap-3 px-4 py-3 lg:px-8">
          <div className="flex items-center gap-2.5">
            <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary text-primary-fg shadow-sm">
              <Layers size={18} />
            </div>
            <div className="hidden sm:block">
              <div className="display text-[16px] leading-5 text-body">Crash-Safe</div>
              <div className="text-[11px] font-medium uppercase tracking-[0.12em] text-faint">Object Store</div>
            </div>
          </div>

          <nav className="ml-2 flex items-center gap-1 lg:ml-8" aria-label="Main">
            {tabs.map((item) => {
              const Icon = item.icon;
              const active = tab === item.id;
              return (
                <button
                  key={item.id}
                  onClick={() => setTab(item.id)}
                  aria-current={active ? 'page' : undefined}
                  className={`inline-flex h-9 items-center gap-2 rounded-lg px-3 text-[13px] font-semibold transition-colors ${
                    active ? 'bg-primary-soft text-primary' : 'text-subtle hover:bg-muted hover:text-body'
                  }`}
                >
                  <Icon size={15} />
                  <span className="hidden xs:inline sm:inline">{item.label}</span>
                </button>
              );
            })}
          </nav>

          <div className="ml-auto flex items-center gap-1.5">
            <span
              className="hidden items-center gap-1.5 rounded-full border border-edge bg-surface px-2.5 py-1 text-[11px] font-semibold text-subtle md:inline-flex"
              title={health ? 'API reachable' : 'API unreachable'}
            >
              {health ? <Wifi size={12} className="text-success" /> : <WifiOff size={12} className="text-danger" />}
              {health ? 'API live' : 'API down'}
            </span>
            <IconButton label={theme === 'dark' ? 'Use light theme' : 'Use dark theme'} onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
              {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
            </IconButton>
            <IconButton label="Demo controls" onClick={() => setDemoOpen(true)}>
              <KeyRound size={16} />
            </IconButton>
            <div className="ml-1 flex items-center gap-2 rounded-full border border-edge bg-surface py-1 pl-1 pr-2">
              <span className="flex h-7 w-7 items-center justify-center rounded-full bg-accent-soft text-[12px] font-bold text-accent-text">
                {(email[0] || 'o').toUpperCase()}
              </span>
              <span className="hidden max-w-[140px] truncate text-[12px] font-medium text-subtle lg:block">{email || 'signed in'}</span>
              <button className="rounded p-1 text-faint hover:bg-muted hover:text-body" onClick={signOut} aria-label="Sign out" title="Sign out">
                <LogOut size={14} />
              </button>
            </div>
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-[1220px] px-4 py-6 lg:px-8 lg:py-8">
        {tab === 'objects' ? <ObjectsTab onUnauthorized={signOut} /> : null}
        {tab === 'failures' ? <FailuresTab onNeedDemo={() => setDemoOpen(true)} /> : null}
        {tab === 'search' ? <SearchTab onUnauthorized={signOut} /> : null}
      </main>

      <Modal open={demoOpen} onClose={() => setDemoOpen(false)} title="Demo controls" subtitle="Reset or seed the dataset. The token is stored locally in this browser.">
        <DemoForm
          value={demoValue}
          onChange={setDemoValue}
          busy={demoBusy}
          onSave={saveDemo}
          onReset={() => runDemo('reset')}
          onSeed={() => runDemo('seed')}
        />
      </Modal>
    </div>
  );
}

function DemoForm({
  value, onChange, busy, onSave, onReset, onSeed,
}: {
  value: string;
  onChange: (v: string) => void;
  busy: string;
  onSave?: () => void;
  onReset: () => void;
  onSeed: () => void;
}) {
  return (
    <div className="space-y-4">
      <Field
        label="Demo token"
        type="password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="X-Demo-Token"
        hint="Never shipped in the image; set it in .env on the host."
      />
      <div className="rounded-xl border border-edge bg-muted p-3 text-[12px] leading-5 text-subtle">
        <strong className="font-semibold text-body">Reset</strong> truncates metadata, clears node chunks, flushes Redis and the WAL.
        <strong className="ml-1 font-semibold text-body">Seed</strong> creates 100 mixed-size objects and signs you in as the demo user.
      </div>
      <div className="flex flex-wrap justify-end gap-2">
        {onSave ? <Button variant="ghost" onClick={onSave}>Save token</Button> : null}
        <Button variant="secondary" loading={busy === 'reset'} disabled={Boolean(busy)} onClick={onReset}>
          Reset data
        </Button>
        <Button loading={busy === 'seed'} disabled={Boolean(busy)} onClick={onSeed}>
          Seed &amp; sign in
        </Button>
      </div>
    </div>
  );
}

function AuthScreen({
  onSignedIn, theme, onToggleTheme, openDemo, demoModal,
}: {
  onSignedIn: (token: string, email: string) => void;
  theme: 'light' | 'dark';
  onToggleTheme: () => void;
  openDemo: () => void;
  demoModal: ReactNode;
}) {
  const toast = useToast();
  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const res = await api<{ token: string }>(`/auth/${mode}`, {
        method: 'POST',
        body: JSON.stringify({ email, password }),
      }, '');
      onSignedIn(res.token, email);
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Sign in failed', 'error');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid min-h-screen lg:grid-cols-[1.05fr_0.95fr]">
      <section className="relative hidden overflow-hidden bg-ink p-10 text-[#EEF0F3] lg:flex lg:flex-col lg:justify-between">
        <div className="absolute inset-0 grid-lines opacity-[0.18]" />
        <div className="relative flex items-center gap-2.5">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary text-primary-fg">
            <Layers size={18} />
          </div>
          <div>
            <div className="display text-[17px] leading-5">Crash-Safe Object Store</div>
            <div className="text-[11px] uppercase tracking-[0.14em] text-[#8B919C]">Three replicas. One truth.</div>
          </div>
        </div>
        <div className="relative max-w-xl">
          <h1 className="display text-[44px] leading-[1.08] tracking-tight">
            Break a node.
            <br />
            Watch the system heal.
          </h1>
          <p className="mt-5 max-w-md text-[15px] leading-7 text-[#A7ADB8]">
            Quorum writes, verified reads, a write-ahead log and background repair. Every byte is checked
            before it is served, and every demo failure is recoverable.
          </p>
          <div className="mt-8 grid max-w-md grid-cols-3 gap-3">
            {[
              ['N=3', 'replicas'],
              ['W=2', 'acks'],
              ['R=1', 'verified'],
            ].map(([value, label]) => (
              <div key={label} className="rounded-xl border border-white/10 bg-white/[0.04] p-3">
                <div className="tnum text-[20px] font-semibold text-[#5FB79A]">{value}</div>
                <div className="text-[11px] uppercase tracking-[0.1em] text-[#8B919C]">{label}</div>
              </div>
            ))}
          </div>
        </div>
        <div className="relative flex items-center gap-2 text-[12px] text-[#8B919C]">
          <span className="h-1.5 w-1.5 rounded-full bg-[#5FB79A]" />
          Docker Compose demo · PostgreSQL · Redis · pgvector
        </div>
      </section>

      <section className="flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-[400px]">
          <div className="mb-6 flex items-center justify-between">
            <div className="flex items-center gap-2 lg:hidden">
              <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-primary text-primary-fg">
                <Layers size={18} />
              </div>
              <span className="display text-[17px]">Crash-Safe</span>
            </div>
            <div className="ml-auto flex gap-1">
              <IconButton label="Toggle theme" onClick={onToggleTheme}>
                {theme === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
              </IconButton>
              <IconButton label="Demo controls" onClick={openDemo}>
                <KeyRound size={16} />
              </IconButton>
            </div>
          </div>

          <div className="card p-6">
            <h2 className="display text-[26px] leading-8 text-body">{mode === 'login' ? 'Welcome back' : 'Create an account'}</h2>
            <p className="mt-1 text-[13px] text-subtle">
              {mode === 'login' ? 'Sign in to browse objects and run failure demos.' : 'Accounts are local to this demo instance.'}
            </p>

            <form className="mt-5 space-y-4" onSubmit={submit}>
              <Field
                label="Email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="you@example.com"
              />
              <Field
                label="Password"
                type="password"
                autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                required
                minLength={8}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="At least 8 characters"
              />
              <Button type="submit" loading={busy} className="w-full">
                {mode === 'login' ? 'Sign in' : 'Create account'}
              </Button>
            </form>

            <div className="mt-4 flex items-center justify-between text-[12px]">
              <button
                className="font-semibold text-primary hover:underline"
                onClick={() => setMode(mode === 'login' ? 'register' : 'login')}
              >
                {mode === 'login' ? 'Create an account' : 'I already have an account'}
              </button>
              <button className="inline-flex items-center gap-1 text-subtle hover:text-body" onClick={openDemo}>
                <KeyRound size={12} />
                Load demo data
              </button>
            </div>
          </div>

          <p className="mt-5 text-center text-[12px] text-faint">
            Demo login after seeding: <span className="font-medium text-subtle">demo@objstore.local</span> / <span className="font-medium text-subtle">demo-password-123</span>
          </p>
        </div>
      </section>
      {demoModal}
    </div>
  );
}
