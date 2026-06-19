import { listPosts } from "@/lib/posts";
import { config } from "@/lib/config";
import { SITE_URL } from "@/lib/seo";

export const dynamic = "force-static";

/**
 * /llms-full.txt — companion to /llms.txt. Concatenates the canonical
 * markdown source of every blog post plus a short site description, so
 * that a model can ingest the entire site with a single fetch.
 *
 * Spec: https://llmstxt.org/
 */
export function GET() {
  const posts = listPosts();

  const sections: string[] = [];

  sections.push(`# ${config.name}`);
  sections.push("");
  sections.push(`> ${config.description}`);
  sections.push("");
  sections.push(
    `${config.name} is an open source cross-harness AI runtime and MCP gateway for Claude Code, Codex, OpenCode, Cursor, Grok, Pi, Gemini CLI, Windsurf, and custom MCP clients. It provides delegations, durable workers, an AI-first task manager, cross-harness memory, browser control, workspace routing, approvals, audit logging, sandboxing, tool restrictions, OAuth credential injection, agent mesh, skill registry, model routing, and broad MCP server wiring. The product is local-first, encrypts secrets at rest with age, and ships under AGPL-3.0-or-later. Built and supported by Don Works.`,
  );
  sections.push("");
  sections.push(`Site: ${SITE_URL}`);
  sections.push(`Source: ${config.githubUrl}`);
  sections.push("");

  sections.push("---");
  sections.push("");

  for (const p of posts) {
    sections.push(`<!-- ${SITE_URL}/blog/${p.slug} -->`);
    sections.push(p.markdown.trim());
    sections.push("");
    sections.push("---");
    sections.push("");
  }

  return new Response(sections.join("\n"), {
    status: 200,
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "public, max-age=300, must-revalidate",
    },
  });
}
