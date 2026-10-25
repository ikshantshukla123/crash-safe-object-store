/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        primary: 'var(--primary)',
        'primary-hover': 'var(--primary-hover)',
        'primary-soft': 'var(--primary-soft)',
        'primary-fg': 'var(--primary-fg)',
        ink: 'var(--ink)',
        accent: 'var(--accent)',
        'accent-text': 'var(--accent-text)',
        'accent-soft': 'var(--accent-soft)',
        verified: 'var(--verified)',
        'verified-soft': 'var(--verified-soft)',
        canvas: 'var(--bg)',
        surface: 'var(--surface)',
        muted: 'var(--surface-muted)',
        edge: 'var(--border)',
        body: 'var(--text)',
        subtle: 'var(--text-muted)',
        faint: 'var(--text-subtle)',
        success: 'var(--success)',
        warning: 'var(--warning)',
        'warning-text': 'var(--warning-text)',
        danger: 'var(--danger)',
      },
      fontFamily: {
        sans: ['"Plus Jakarta Sans"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        display: ['Fraunces', 'ui-serif', 'Georgia', 'serif'],
      },
      boxShadow: {
        card: '0 1px 2px rgb(15 27 45 / 0.04), 0 12px 32px rgb(15 27 45 / 0.05)',
        lift: '0 2px 4px rgb(15 27 45 / 0.06), 0 20px 48px rgb(15 27 45 / 0.09)',
      },
    },
  },
  plugins: [],
};
