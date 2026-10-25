import { createContext, ReactNode, useCallback, useContext, useMemo, useState } from 'react';
import { AlertTriangle, CheckCircle2, Info, X } from 'lucide-react';

type ToastKind = 'success' | 'error' | 'info';
type Toast = { id: number; message: string; kind: ToastKind };

const ToastContext = createContext<(message: string, kind?: ToastKind) => void>(() => {});

export function useToast() {
  return useContext(ToastContext);
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const push = useCallback((message: string, kind: ToastKind = 'info') => {
    const id = Date.now() + Math.random();
    setToasts((current) => [...current, { id, message, kind }]);
    window.setTimeout(() => {
      setToasts((current) => current.filter((t) => t.id !== id));
    }, 4200);
  }, []);

  const value = useMemo(() => push, [push]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[min(360px,calc(100vw-2rem))] flex-col gap-2">
        {toasts.map((toast) => (
          <div key={toast.id} className="pointer-events-auto card flex items-start gap-2.5 p-3 text-[13px] rise">
            <span className={toast.kind === 'error' ? 'mt-0.5 text-danger' : toast.kind === 'success' ? 'mt-0.5 text-success' : 'mt-0.5 text-verified'}>
              {toast.kind === 'error' ? <AlertTriangle size={16} /> : toast.kind === 'success' ? <CheckCircle2 size={16} /> : <Info size={16} />}
            </span>
            <span className="flex-1 text-body">{toast.message}</span>
            <button
              className="rounded p-0.5 text-faint hover:bg-muted hover:text-body"
              aria-label="Dismiss"
              onClick={() => setToasts((current) => current.filter((t) => t.id !== toast.id))}
            >
              <X size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
