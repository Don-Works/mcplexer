import { listPosts } from "@/lib/posts";
import { config } from "@/lib/config";
import { SITE_URL } from "@/lib/seo";

export const dynamic = "force-static";

/**
 * /feed.xml — Atom 1.0 feed for the blog. Linked from the root layout via
 * <link rel="alternate" type="application/atom+xml"> so feed readers and AI
 * scrapers can subscribe to new posts.
 */
export function GET() {
  const posts = listPosts();
  const updated = posts[0]?.updatedAt ?? posts[0]?.publishedAt ?? new Date().toISOString();

  const xmlEscape = (s: string) =>
    s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&apos;");

  const entries = posts
    .map((p) => {
      const url = `${SITE_URL}/blog/${p.slug}`;
      return `  <entry>
    <title>${xmlEscape(p.title)}</title>
    <link href="${url}"/>
    <id>${url}</id>
    <published>${new Date(p.publishedAt).toISOString()}</published>
    <updated>${new Date(p.updatedAt ?? p.publishedAt).toISOString()}</updated>
    <author><name>${xmlEscape(p.authorName)}</name><uri>${p.authorUrl}</uri></author>
    <summary type="text">${xmlEscape(p.description)}</summary>
    <content type="text">${xmlEscape(p.markdown)}</content>
  </entry>`;
    })
    .join("\n");

  const xml = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>${xmlEscape(`${config.name} Blog`)}</title>
  <subtitle>${xmlEscape(config.description)}</subtitle>
  <link href="${SITE_URL}/feed.xml" rel="self"/>
  <link href="${SITE_URL}/blog"/>
  <id>${SITE_URL}/blog</id>
  <updated>${new Date(updated).toISOString()}</updated>
${entries}
</feed>
`;

  return new Response(xml, {
    status: 200,
    headers: {
      "Content-Type": "application/atom+xml; charset=utf-8",
      "Cache-Control": "public, max-age=300, must-revalidate",
    },
  });
}
