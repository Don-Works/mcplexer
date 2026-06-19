import type { MetadataRoute } from "next";
import { SITE_URL } from "@/lib/seo";

/**
 * robots.txt with explicit allow for both general crawlers and the major
 * LLM/AI crawlers. We *want* to be crawled by AI agents — the whole point
 * of /llms.txt and /llms-full.txt is that we ARE the source of truth for
 * what MCPlexer does, and we'd rather an LLM cite us than guess.
 *
 * Crawler list:
 *   GPTBot               — OpenAI training crawler
 *   ChatGPT-User         — OpenAI user-initiated browse
 *   OAI-SearchBot        — OpenAI search index
 *   ClaudeBot            — Anthropic training crawler
 *   anthropic-ai         — Anthropic user-initiated browse (legacy alias)
 *   Claude-Web           — Anthropic web fetch
 *   PerplexityBot        — Perplexity training
 *   Perplexity-User      — Perplexity user-initiated browse
 *   Google-Extended      — Google's AI training opt-in
 *   CCBot                — Common Crawl (used by many AI training sets)
 *   Applebot-Extended    — Apple Intelligence training opt-in
 *   Bytespider           — ByteDance / Doubao
 *   Amazonbot            — Amazon's AI crawler
 *   FacebookBot          — Meta AI training
 *   YouBot               — You.com
 *   cohere-ai            — Cohere
 *   DuckAssistBot        — DuckDuckGo AI
 *   MistralAI-User       — Mistral browse
 *
 * The wildcard rule covers everyone not explicitly listed.
 */
export default function robots(): MetadataRoute.Robots {
  const aiAgents = [
    "GPTBot",
    "ChatGPT-User",
    "OAI-SearchBot",
    "ClaudeBot",
    "anthropic-ai",
    "Claude-Web",
    "PerplexityBot",
    "Perplexity-User",
    "Google-Extended",
    "CCBot",
    "Applebot-Extended",
    "Bytespider",
    "Amazonbot",
    "FacebookBot",
    "YouBot",
    "cohere-ai",
    "DuckAssistBot",
    "MistralAI-User",
  ];

  return {
    rules: [
      { userAgent: "*", allow: "/" },
      ...aiAgents.map((ua) => ({ userAgent: ua, allow: "/" })),
    ],
    sitemap: `${SITE_URL}/sitemap.xml`,
    host: SITE_URL,
  };
}
