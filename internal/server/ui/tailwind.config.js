// Tailwind config for the attractor web UI (web-ui-spec). The UI ships as a
// single go:embed'd page; this compiles a tree-shaken utility stylesheet from
// the classes used in index.html, injected into the page by serveUI.
//
// Two deliberate choices keep this ADDITIVE and theme-correct:
//  1. preflight OFF — the existing hand-styled UI depends on default element
//     styling; Tailwind's reset would clobber it. Utilities are added on top;
//     sections migrate to Tailwind incrementally (T1..T5).
//  2. colours/spacing/fonts map onto the existing CSS custom properties, which
//     already switch light/dark (data-theme + prefers-color-scheme). So a
//     utility like `bg-surface-1` is theme-aware for free, and `dark:` variants
//     are rarely needed. darkMode still keyed to [data-theme="dark"] for the
//     cases that want an explicit dark-only rule.
module.exports = {
  content: ["./index.html"],
  darkMode: ["selector", '[data-theme="dark"]'],
  corePlugins: { preflight: false },
  theme: {
    extend: {
      colors: {
        surface: {
          0: "var(--surface-0)",
          1: "var(--surface-1)",
          2: "var(--surface-2)",
        },
        ink: "var(--text)",
        muted: "var(--text-muted)",
        line: "var(--border)",
        accent: "var(--accent)",
        attention: "var(--attention-ink)",
        state: {
          success: "var(--state-success)",
          failed: "var(--state-failed)",
          running: "var(--state-running)",
          queued: "var(--state-queued)",
          "needs-human": "var(--state-needs-human)",
          retrying: "var(--state-retrying)",
          cancelled: "var(--state-cancelled)",
        },
      },
      fontFamily: { base: "var(--font-base)" },
      fontSize: {
        sm: "var(--font-sm)",
        base: "var(--font-base)",
        lg: "var(--font-lg)",
      },
      spacing: {
        1: "var(--space-1)",
        2: "var(--space-2)",
        3: "var(--space-3)",
        4: "var(--space-4)",
        5: "var(--space-5)",
      },
      borderColor: { DEFAULT: "var(--border)" },
    },
  },
  plugins: [],
};
