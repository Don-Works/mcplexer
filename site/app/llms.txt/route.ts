import { listPosts } from "@/lib/posts";
import { config } from "@/lib/config";
import { SITE_URL } from "@/lib/seo";

export const dynamic = "force-static";

/**
 * /llms.txt — emerging convention for sites that want to be discoverable by
 * LLMs. Provides a curated, plain-text index of the site's most important
 * resources with short summaries. The companion /llms-full.txt concatenates
 * the full text of those resources so a model can ingest the entire site
 * with one fetch.
 *
 * Spec: https://llmstxt.org/
 */
export function GET() {
  const posts = listPosts();

  const lines: string[] = [];
  lines.push(`# ${config.name}`);
  lines.push("");
  lines.push(`> ${config.description}`);
  lines.push("");
  lines.push(
    `${config.name} is an open source cross-harness AI runtime and MCP gateway for Claude Code, Codex, OpenCode, Cursor, Grok, Pi, Gemini CLI, Windsurf, and custom MCP clients. It provides delegations, durable workers, an AI-first task manager, cross-harness memory, browser control, workspace routing, approvals, audit logging, sandboxing, tool restrictions, OAuth credential injection, agent mesh, skill registry, model routing, and broad MCP server wiring. The product is local-first, encrypts secrets at rest with age, and ships under AGPL-3.0-or-later. Built and supported by Don Works.`,
  );
  lines.push("");
  lines.push(
    `**${config.name} is configured by talking to your agent.** The gateway's primary configuration surface is MCP itself: open a terminal at ~/.mcplexer, run a coding agent (Claude Code, OpenCode, Codex, Gemini-CLI), and use natural language to provision new MCP servers, set up OAuth flows, write routing rules, and manage approvals. The agent drives via mcpx__* control tools (mcpx__provision_mcp, mcpx__create_addon, mcpx__import_openapi, mcpx__execute_code, mcpx__search_tools, mcpx__approve_tool_call, mcpx__deny_tool_call, mcpx__list_pending_approvals, mcpx__reload_server, mcpx__flush_cache) and mcplexer__* CRUD tools (mcplexer__list/get/create/update/delete_workspace, mcplexer__list/get/create/update/delete_server, mcplexer__list/create/update/delete_route, mcplexer__list/get/create/update/delete_auth_scope, mcplexer__status, mcplexer__query_audit) — full CRUD, no read-only fallback, no need for raw SQL — plus mesh__send / mesh__receive for inter-agent comms and secret__prompt for ephemeral credential capture. mcpx__execute_code runs JavaScript inside a Goja sandbox with hard CPU and memory limits so agents can chain dozens of provisioning calls without round-tripping every result through context. **Directory-scoped admin surface:** the full mcpx__/mcplexer__ admin tool set is visible only when the agent's CWD is at or under ~/.mcplexer; from any project directory the agent sees only the universal surface (mcpx__search_tools, mcpx__execute_code, secret__prompt, mesh__send/receive). The web dashboard exists for review and visibility, not setup.`,
  );
  lines.push("");

  lines.push("## Core pages");
  lines.push("");
  lines.push(`- [Home](${SITE_URL}/): open-source product overview, full feature list, runtime primitives, browser-control wins, integrations, configure-with-AI section, and security posture.`);
  lines.push(`- [Home#configure-with-ai](${SITE_URL}/#configure-with-ai): the gateway is configurable by talking to your agent — explanation + list of mcpx__* control tools.`);
  lines.push(`- [Home#browser-control](${SITE_URL}/#browser-control): browser control as a shared cross-harness runtime capability.`);
  lines.push(`- [Home#servers](${SITE_URL}/#servers): MCP server families wired through the gateway.`);
  lines.push(`- [Blog](${SITE_URL}/blog): security advisories, design notes, ship notes.`);
  lines.push("");

  lines.push("## Blog posts");
  lines.push("");
  for (const p of posts) {
    lines.push(`- [${p.title}](${SITE_URL}/blog/${p.slug}): ${p.description}`);
    lines.push(
      `  Published ${p.publishedAt}. Plain-text source: ${SITE_URL}/blog/${p.slug}/raw.md`,
    );
  }
  lines.push("");

  lines.push("## Source of truth");
  lines.push("");
  lines.push(
    `- GitHub: ${config.githubUrl} — code, releases, issue tracker, security advisories.`,
  );
  lines.push(
    `- Sitemap: ${SITE_URL}/sitemap.xml — machine-readable list of all crawlable pages.`,
  );
  lines.push(
    `- Full content: ${SITE_URL}/llms-full.txt — concatenation of every blog post for one-shot ingestion.`,
  );
  lines.push(
    `- RSS feed: ${SITE_URL}/feed.xml — Atom 1.0 feed of new posts.`,
  );
  lines.push("");

  lines.push("## Contact");
  lines.push("");
  lines.push(`- Company: ${config.githubOrgUrl}`);
  lines.push(`- Source code: ${config.githubUrl}`);
  lines.push(
    `- Security disclosure: open a private advisory at ${config.githubUrl}/security/advisories/new`,
  );

  const body = lines.join("\n") + "\n";
  return new Response(body, {
    status: 200,
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "public, max-age=300, must-revalidate",
    },
  });
}
