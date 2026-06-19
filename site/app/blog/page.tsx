import Link from "next/link";
import type { Metadata } from "next";
import { ArrowRight, Calendar, Clock, Tag } from "lucide-react";
import { listPosts } from "@/lib/posts";
import { pageMetadata, SITE_URL } from "@/lib/seo";
import { JsonLd } from "@/components/json-ld";
import { config } from "@/lib/config";

export const metadata: Metadata = pageMetadata({
  title: "Blog",
  description:
    "Notes from the MCPlexer team on cross-harness AI runtimes, MCP security, and protocol design.",
  path: "/blog",
});

export default function BlogIndexPage() {
  const posts = listPosts();

  const blogJsonLd = {
    "@context": "https://schema.org",
    "@type": "Blog",
    name: `${config.name} Blog`,
    url: `${SITE_URL}/blog`,
    description:
      "Notes from the MCPlexer team on cross-harness AI runtimes, MCP security, and protocol design.",
    publisher: {
      "@type": "Organization",
      name: config.name,
      url: SITE_URL,
    },
    blogPost: posts.map((p) => ({
      "@type": "BlogPosting",
      headline: p.title,
      description: p.description,
      url: `${SITE_URL}/blog/${p.slug}`,
      datePublished: p.publishedAt,
      dateModified: p.updatedAt ?? p.publishedAt,
      author: { "@type": "Person", name: p.authorName, url: p.authorUrl },
    })),
  };

  const breadcrumbJsonLd = {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: [
      { "@type": "ListItem", position: 1, name: "Home", item: SITE_URL },
      {
        "@type": "ListItem",
        position: 2,
        name: "Blog",
        item: `${SITE_URL}/blog`,
      },
    ],
  };

  return (
    <>
      <JsonLd data={[blogJsonLd, breadcrumbJsonLd]} />

      <article className="mx-auto max-w-4xl px-4 sm:px-6 pt-32 pb-20">
        <header className="mb-14">
          <p className="text-xs font-mono uppercase tracking-wider text-text-muted mb-3">
            <Link
              href="/"
              className="hover:text-cyan transition-colors"
            >
              Home
            </Link>{" "}
            / Blog
          </p>
          <h1 className="text-3xl sm:text-4xl font-bold tracking-tight mb-3">
            Blog
          </h1>
          <p className="text-sm text-text-muted leading-relaxed max-w-2xl">
            Disclosures, design notes, and what we ship. Built for humans and
            for the machines that read this in your stead — every post has a{" "}
            <Link
              href="/llms.txt"
              className="text-cyan hover:text-cyan-light transition-colors"
            >
              plain-text mirror
            </Link>
            .
          </p>
        </header>

        <ul className="flex flex-col gap-3">
          {posts.map((post) => (
            <li key={post.slug}>
              <Link
                href={`/blog/${post.slug}`}
                className="group block bg-surface border border-border p-6 hover:border-cyan/40 transition-colors"
              >
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[10px] font-mono uppercase tracking-wider text-text-muted mb-3">
                  <span className="inline-flex items-center gap-1.5">
                    <Calendar className="w-3 h-3" />
                    <time dateTime={post.publishedAt}>
                      {formatDate(post.publishedAt)}
                    </time>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <Clock className="w-3 h-3" />
                    {post.readingTimeMinutes} min read
                  </span>
                  {post.tags.length > 0 && (
                    <span className="inline-flex items-center gap-1.5">
                      <Tag className="w-3 h-3" />
                      {post.tags.join(" · ")}
                    </span>
                  )}
                </div>
                <h2 className="text-xl font-bold tracking-tight mb-2 group-hover:text-cyan transition-colors">
                  {post.title}
                </h2>
                <p className="text-sm text-text-muted leading-relaxed mb-4">
                  {post.excerpt}
                </p>
                <span className="inline-flex items-center gap-1.5 text-xs text-cyan group-hover:text-cyan-light transition-colors">
                  Read post
                  <ArrowRight className="w-3 h-3 transition-transform group-hover:translate-x-0.5" />
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </article>
    </>
  );
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString("en-GB", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}
