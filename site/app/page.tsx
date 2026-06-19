import Link from "next/link";
import type { Metadata } from "next";
import { JsonLd } from "@/components/json-ld";
import { config } from "@/lib/config";
import { pageMetadata, SITE_URL } from "@/lib/seo";
import {
  Activity,
  ArrowRight,
  Bot,
  BrainCircuit,
  CheckCircle2,
  FileSearch,
  Gauge,
  KeyRound,
  ListTodo,
  MousePointerClick,
  Network,
  PlugZap,
  Route,
  ScrollText,
  Search,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Terminal,
  Workflow,
  type LucideIcon,
} from "lucide-react";

export const metadata: Metadata = pageMetadata({
  title: config.name,
  description: config.description,
  path: "/",
});

type Feature = {
  icon: LucideIcon;
  title: string;
  body: string;
  label: string;
};

const statusItems = [
  "Delegation ledger and worker review loop live",
  "AI-first tasks, memory, mesh, and skills wired",
  "Browser control available across harnesses",
  "Approvals, audit, workspaces, and guards active",
];

const capabilityChips = [
  "Claude Code",
  "Codex",
  "OpenCode",
  "Cursor",
  "Grok",
  "Pi",
  "Gemini CLI",
  "Windsurf",
  "custom MCP clients",
  "local daemon",
  "AGPL source",
];

const headlineFeatures: Feature[] = [
  {
    icon: Search,
    label: "Harness",
    title: "One slim tool surface",
    body: "Every harness sees discovery, batched execution, and secret refs instead of loading a giant static tool list into context.",
  },
  {
    icon: Workflow,
    label: "Delegations",
    title: "Cross-harness delegation",
    body: "Delegate between models and harnesses with ease: keep the parent strategic while workers inspect files, run tests, draft changes, and return for scored review.",
  },
  {
    icon: Bot,
    label: "Workers",
    title: "Durable AI workers",
    body: "Manual, scheduled, mesh-triggered, and delegation-backed workers with model profiles, caps, approvals, and live run output.",
  },
  {
    icon: ListTodo,
    label: "Tasks",
    title: "AI-first task manager",
    body: "A work ledger designed for LLMs: stable IDs, leases, status events, offers, assignments, context packets, and attachments.",
  },
  {
    icon: BrainCircuit,
    label: "Memory",
    title: "Cross-harness memory",
    body: "Persistent facts and notes survive model swaps, client swaps, and machines, with consolidation, invalidation, and sharing.",
  },
  {
    icon: MousePointerClick,
    label: "Browser",
    title: "Remote-capable browser control",
    body: "Drive visible Chrome, Playwright sessions, and desktop surfaces through the same routed MCP layer, including worker and peer workflows.",
  },
  {
    icon: Route,
    label: "Workspaces",
    title: "Directory-scoped routing",
    body: "The real working directory decides which servers, tools, auth scopes, routes, and approval policies are available.",
  },
  {
    icon: ShieldCheck,
    label: "Restrictions",
    title: "Sandboxing and tool gates",
    body: "Capability presets, allowlists, deny rules, shell guard, sanitizer guard, schedule guard, sandbox guard, and downstream sandboxing.",
  },
  {
    icon: CheckCircle2,
    label: "Approvals",
    title: "No self-approval shortcut",
    body: "Sensitive calls pause for review in another session or the dashboard, with OS notifications and a worker approval queue.",
  },
  {
    icon: ScrollText,
    label: "Audit",
    title: "Everything leaves evidence",
    body: "Tool calls, worker dispatches, model sends, denials, approvals, mesh triggers, memory events, and route decisions are searchable.",
  },
  {
    icon: PlugZap,
    label: "Servers",
    title: "MCP server fabric",
    body: "GitHub, Linear, Slack, Gmail, Calendar, Postgres, Vercel, WordPress, WooCommerce, Reddit, Telegram, browsers, and custom servers.",
  },
  {
    icon: KeyRound,
    label: "Secrets",
    title: "Auth scopes, not pasted tokens",
    body: "OAuth and API keys are encrypted at rest, injected only into matching routes, and kept out of transcripts through secret refs.",
  },
  {
    icon: Network,
    label: "Mesh",
    title: "Agent-to-agent coordination",
    body: "Agents discover peers, exchange findings, offer tasks, send targeted questions, and coordinate across paired machines.",
  },
  {
    icon: Sparkles,
    label: "Skills",
    title: "Reusable playbooks",
    body: "Search, install, publish, compose, and sync skills so agents load only the guidance they need for the current job.",
  },
  {
    icon: Gauge,
    label: "Models",
    title: "Model routing and ranks",
    body: "Capacity routing, provider profiles, cost dashboards, review scores, and leaderboards show which cheap models actually work.",
  },
  {
    icon: Activity,
    label: "Dashboard",
    title: "Installable operations view",
    body: "PWA dashboard for approvals, audit, live signals, workspaces, routes, servers, model providers, backups, and recovery.",
  },
];

const runtimePanels = [
  {
    eyebrow: "Plan",
    title: "Delegate the work that burns context",
    body: "Frontier sessions define architecture, scope, and acceptance criteria. Workers do broad inspection, implementation, test loops, and log triage in bounded contexts.",
  },
  {
    eyebrow: "Run",
    title: "Keep agents working across sessions",
    body: "Tasks, memory, skills, and mesh give agents durable coordination primitives instead of making every chat rediscover the same project state.",
  },
  {
    eyebrow: "Control",
    title: "Expose power without losing the boundary",
    body: "Workspace routing, approvals, audit, tool restrictions, sandboxing, and encrypted auth scopes make powerful MCP servers usable in real repos.",
  },
];

const browserWins = [
  "Cross-harness: Claude Code, Codex, OpenCode, Cursor, Grok, Pi, Gemini, and workers can all use it.",
  "Remote-capable: a paired machine or worker can use the browser near the login, network, display, or local service.",
  "Semantic-first: snapshots, find, click text, type, press, tabs, and screenshots as fallback.",
  "Audited and scoped: browser actions are still routed tool calls with approvals and workspace policy.",
  "Visible or clean-room: use real Chrome tabs for human-visible state, or Playwright sessions for automation.",
];

const integrationGroups = [
  ["Code", "GitHub", "Linear", "ClickUp", "Vercel"],
  ["Comms", "Slack", "Gmail", "Calendar", "Telegram", "OpenWA"],
  ["Data", "Postgres", "SQLite", "fetch", "customer MCP"],
  ["Browser", "agent_browser", "Playwright", "Hammerspoon"],
  ["Content", "WordPress", "WooCommerce", "Obsidian", "Excalidraw"],
  ["AI", "LM Studio", "model providers", "skills", "memory", "task", "mesh"],
];

export default function HomePage() {
  const softwareJsonLd = {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    name: config.name,
    description: config.description,
    applicationCategory: "DeveloperApplication",
    applicationSubCategory: "Cross-harness AI runtime",
    operatingSystem: "macOS, Linux",
    url: SITE_URL,
    offers: {
      "@type": "Offer",
      price: "0",
      priceCurrency: "USD",
      description:
        "Open source under AGPL-3.0-or-later. Commercial license exceptions are available from Don Works.",
    },
    sameAs: [config.githubUrl],
    publisher: {
      "@type": "Organization",
      name: "Don Works",
      url: config.githubOrgUrl,
    },
  };

  const faqJsonLd = {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    mainEntity: [
      {
        "@type": "Question",
        name: "What is MCPlexer?",
        acceptedAnswer: {
          "@type": "Answer",
          text: "MCPlexer is an open source cross-harness AI runtime and MCP gateway for delegations, workers, tasks, memory, browser control, approvals, audit, workspaces, sandboxing, and routed tool access.",
        },
      },
      {
        "@type": "Question",
        name: "Which AI clients work with MCPlexer?",
        acceptedAnswer: {
          "@type": "Answer",
          text: "MCPlexer works with MCP-compatible clients and harnesses including Claude Code, Codex, OpenCode, Cursor, Grok, Pi, Gemini CLI, Windsurf, and custom clients.",
        },
      },
      {
        "@type": "Question",
        name: "Why is MCPlexer browser control different?",
        acceptedAnswer: {
          "@type": "Answer",
          text: "Browser control is exposed through the gateway, so it is cross-harness, worker-capable, remote-capable through paired peers, and subject to the same routing, approvals, and audit as other MCP tools.",
        },
      },
    ],
  };

  return (
    <>
      <JsonLd data={[softwareJsonLd, faqJsonLd]} />

      <section className="mcpx-hero relative flex min-h-[82svh] items-center justify-center overflow-hidden border-b border-border bg-bg px-4 py-16 sm:px-6 sm:py-20">
        <div className="mcpx-hero-grid-upright" aria-hidden="true" />
        <div className="mcpx-hero-grid-plane" aria-hidden="true" />
        <div className="scanlines pointer-events-none absolute inset-0" />

        <div className="relative z-10 mx-auto grid w-full max-w-6xl grid-cols-1 items-center gap-10 lg:grid-cols-[1.05fr_0.95fr]">
          <div className="text-center lg:text-left">
            <p className="mb-6 text-[10px] font-medium uppercase text-cyan-light">
              open source agent infrastructure
            </p>
            <h1 className="mcpx-hero-title text-4xl font-bold leading-[0.98] text-text sm:text-6xl lg:text-7xl">
              Cross-harness runtime for serious AI work.
            </h1>
            <p className="mt-7 max-w-3xl text-base leading-relaxed text-text-muted sm:text-lg lg:max-w-2xl">
              MCPlexer gives Claude Code, Codex, OpenCode, Cursor, Grok,
              Pi, Gemini, and other MCP clients the same operating layer:
              delegations, workers, tasks, memory, browser control, approvals,
              audit, workspaces, sandboxing, and routed MCP servers.
            </p>

            <div className="mt-10 flex flex-col items-center gap-4 sm:flex-row lg:items-start">
              <Link
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="mcpx-primary-cta inline-flex w-full items-center justify-center px-8 py-4 text-sm font-semibold text-bg sm:w-auto"
              >
                View source
              </Link>
              <a
                href="#features"
                className="inline-flex w-full items-center justify-center border border-cyan/70 px-8 py-4 text-sm font-semibold text-cyan-light shadow-[0_0_14px_rgba(46,164,224,0.22)] transition-colors hover:border-cyan-light hover:text-text sm:w-auto"
              >
                See features
              </a>
            </div>
          </div>

          <div className="border border-border bg-surface/90 p-5 shadow-[0_0_36px_rgba(46,164,224,0.08)]">
            <div className="mb-4 flex items-center justify-between border-b border-border pb-3">
              <p className="text-xs text-text-muted">mcplexer status</p>
              <span className="flex items-center gap-2 text-xs text-green">
                <span className="h-2 w-2 rounded-full bg-green" />
                live
              </span>
            </div>
            <ul className="space-y-3">
              {statusItems.map((item) => (
                <li key={item} className="flex items-start gap-3 text-sm text-text-muted">
                  <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-green" />
                  <span>{item}</span>
                </li>
              ))}
            </ul>
            <div className="mt-5 border-t border-border pt-4">
              <p className="text-[10px] uppercase text-text-dim">universal surface</p>
              <p className="mt-2 font-mono text-xs text-cyan">
                mcpx__search_tools + mcpx__execute_code + secret refs
              </p>
            </div>
          </div>
        </div>
      </section>

      <section className="border-b border-border bg-bg-alt">
        <div className="mx-auto max-w-6xl px-4 py-8 sm:px-6">
          <p className="mb-4 text-center text-sm font-semibold text-cyan">
            Built for the harnesses and tools agents actually use
          </p>
          <ul className="flex flex-wrap items-center justify-center gap-2" aria-label="Supported surfaces">
            {capabilityChips.map((chip) => (
              <li
                key={chip}
                className="border border-cyan/30 bg-surface-elevated px-3 py-1.5 text-xs font-semibold text-text"
              >
                {chip}
              </li>
            ))}
          </ul>
        </div>
      </section>

      <section id="features" className="py-20 sm:py-28">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold text-cyan">Full feature list</p>
          <h2 className="mt-3 text-2xl font-bold sm:text-3xl">
            One runtime for the whole agent operating model.
          </h2>
          <p className="mt-4 max-w-3xl text-sm leading-relaxed text-text-muted">
            The important part is not a single dashboard. It is that the same
            primitives are available across harnesses: work can be delegated,
            remembered, audited, approved, routed, restricted, and continued.
          </p>

          <div className="stagger mt-10 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {headlineFeatures.map(({ icon: Icon, label, title, body }) => (
              <article
                key={title}
                className="border border-border bg-surface p-5 transition-colors hover:border-border-hover"
              >
                <div className="mb-4 flex items-start gap-3">
                  <span className="flex h-9 w-9 shrink-0 items-center justify-center border border-cyan/20 bg-cyan/10 text-cyan">
                    <Icon className="h-4 w-4" />
                  </span>
                  <div>
                    <p className="text-[10px] uppercase text-text-dim">{label}</p>
                    <h3 className="mt-1 text-sm font-semibold leading-snug text-text">{title}</h3>
                  </div>
                </div>
                <p className="text-xs leading-relaxed text-text-muted">{body}</p>
              </article>
            ))}
          </div>
        </div>
      </section>

      <section id="runtime" className="border-y border-border bg-surface-elevated py-12 sm:py-16">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            {runtimePanels.map((panel) => (
              <article key={panel.title} className="border border-border bg-surface p-6">
                <p className="text-[10px] uppercase text-text-dim">{panel.eyebrow}</p>
                <h3 className="mt-2 text-lg font-bold sm:text-xl">
                  {panel.title}
                </h3>
                <p className="mt-3 text-sm leading-relaxed text-text-muted">{panel.body}</p>
              </article>
            ))}
          </div>
        </div>
      </section>

      <section id="browser-control" className="py-20 sm:py-28">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <div className="grid grid-cols-1 gap-8 lg:grid-cols-[0.85fr_1.15fr]">
            <div>
              <p className="text-sm font-semibold text-cyan">Browser control</p>
              <h2 className="mt-3 text-2xl font-bold sm:text-3xl">
                Beats Claude Chrome because it is a shared runtime capability.
              </h2>
              <p className="mt-4 text-sm leading-relaxed text-text-muted">
                A browser tied to one chat helps that chat. MCPlexer exposes
                browser control as routed infrastructure, so the capability can
                be used by any harness, by durable workers, and by paired
                machines while still being auditable.
              </p>
            </div>
            <div className="border border-border bg-surface p-6">
              <ul className="space-y-3">
                {browserWins.map((win) => (
                  <li key={win} className="flex items-start gap-3 text-sm text-text-muted">
                    <MousePointerClick className="mt-0.5 h-4 w-4 shrink-0 text-cyan" />
                    <span>{win}</span>
                  </li>
                ))}
              </ul>
            </div>
          </div>
        </div>
      </section>

      <section id="servers" className="border-y border-border bg-bg-alt py-20 sm:py-28">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold text-cyan">MCP server fabric</p>
          <h2 className="mt-3 text-2xl font-bold sm:text-3xl">
            Wire up broad capability without flooding every context.
          </h2>
          <p className="mt-4 max-w-3xl text-sm leading-relaxed text-text-muted">
            Downstream servers sit behind routes, auth scopes, workspace
            policy, approvals, and audit. Agents discover what they need when
            they need it.
          </p>
          <div className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {integrationGroups.map(([group, ...items]) => (
              <article key={group} className="border border-border bg-surface p-6">
                <h3 className="text-sm font-semibold text-text">{group}</h3>
                <div className="mt-4 flex flex-wrap gap-2">
                  {items.map((item) => (
                    <span key={item} className="border border-cyan/20 bg-cyan/5 px-2.5 py-1 text-xs text-cyan">
                      {item}
                    </span>
                  ))}
                </div>
              </article>
            ))}
          </div>
        </div>
      </section>

      <section id="configure-with-ai" className="py-20 sm:py-28">
        <div className="mx-auto max-w-5xl px-4 sm:px-6">
          <p className="text-sm font-semibold text-cyan">AI-native setup</p>
          <h2 className="mt-3 text-2xl font-bold sm:text-3xl">
            Configure it by talking to your agent.
          </h2>
          <p className="mt-4 max-w-3xl text-sm leading-relaxed text-text-muted">
            Inside <code className="border border-cyan/10 bg-cyan/5 px-1.5 py-0.5 text-xs text-cyan">~/.mcplexer</code>,
            the agent can provision servers, import OpenAPI specs, write
            routes, set auth scopes, inspect audit, and manage approvals.
            In ordinary project repos, it only sees the slim universal surface.
          </p>
          <div className="mt-8 border border-border bg-surface p-6">
            <div className="mb-4 flex items-center gap-2">
              <Terminal className="h-4 w-4 text-cyan" />
              <p className="text-xs text-text-muted">example prompt</p>
            </div>
            <p className="text-sm leading-relaxed text-text">
              <span className="text-cyan">&gt;</span> Wire GitHub, Linear,
              Gmail, Calendar, Postgres, browser control, and a cheap worker
              model into this workspace. Require approval for write tools and
              give Codex the same surface Claude Code gets.
            </p>
          </div>
        </div>
      </section>

      <section id="security" className="border-y border-border bg-surface-elevated py-20 sm:py-28">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <p className="text-sm font-semibold text-cyan">Power with boundaries</p>
          <h2 className="mt-3 text-2xl font-bold sm:text-3xl">
            Serious agents need serious constraints.
          </h2>
          <div className="mt-8 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {[
              "Workspace-scoped routing",
              "Human approvals with self-approval blocked",
              "Searchable audit with redaction",
              "Encrypted auth scopes and secret refs",
              "Sandboxing and tool restrictions",
              "Downstream command guard",
            ].map((item) => (
              <div key={item} className="flex items-center gap-3 border border-border bg-surface p-4">
                <ShieldAlert className="h-4 w-4 shrink-0 text-amber" />
                <span className="text-sm text-text-muted">{item}</span>
              </div>
            ))}
          </div>
          <Link
            href="/blog/mcp-stdio-rce-mitigations"
            className="mt-8 inline-flex items-center gap-2 border border-cyan/30 px-5 py-2.5 text-sm text-cyan hover:bg-cyan/5"
          >
            Read the MCP STDIO RCE mitigation note
            <ArrowRight className="h-4 w-4" />
          </Link>
        </div>
      </section>

      <section className="py-20 sm:py-28">
        <div className="mx-auto max-w-6xl px-4 sm:px-6">
          <div className="border border-border bg-surface-elevated p-10 text-center shadow-[0_0_30px_rgba(46,164,224,0.12)] sm:p-16">
            <h2 className="text-2xl font-bold sm:text-3xl">
              Build agent systems that continue across tools, models, and machines.
            </h2>
            <p className="mx-auto mt-4 max-w-2xl text-sm leading-relaxed text-text-muted">
              MCPlexer is open source infrastructure for people pushing past
              one model, one chat, and one tool list.
            </p>
            <div className="mt-8 flex flex-col items-center justify-center gap-3 sm:flex-row">
              <Link
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="bg-cyan px-6 py-3 text-sm font-medium text-bg hover:bg-cyan-light"
              >
                View source
              </Link>
              <Link
                href="/llms.txt"
                className="inline-flex items-center gap-2 border border-cyan/30 px-6 py-3 text-sm font-medium text-cyan hover:bg-cyan/5"
              >
                LLM index
                <FileSearch className="h-4 w-4" />
              </Link>
            </div>
          </div>
        </div>
      </section>
    </>
  );
}
