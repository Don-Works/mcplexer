import type { Metadata } from "next";
import { config } from "@/lib/config";

export const SITE_URL = "https://mcplexer.com";

export function absUrl(path: string): string {
  if (path.startsWith("http")) return path;
  return `${SITE_URL}${path.startsWith("/") ? path : `/${path}`}`;
}

type PageMetaInput = {
  title: string;
  description: string;
  path: string;
  /** "article" for blog posts, "website" for everything else. */
  type?: "website" | "article";
  /** ISO publish date — only for articles. */
  publishedTime?: string;
  /** ISO update date — only for articles. */
  modifiedTime?: string;
  authorName?: string;
  tags?: string[];
};

/**
 * Build a Metadata object with consistent canonical, OG, Twitter, and robots
 * fields. Pages should call this rather than hand-rolling so the AIO surface
 * stays uniform across the site.
 */
export function pageMetadata(input: PageMetaInput): Metadata {
  const url = absUrl(input.path);
  const fullTitle =
    input.title === config.name
      ? `${config.name} — ${config.tagline}`
      : `${input.title} · ${config.name}`;

  const meta: Metadata = {
    // `absolute` bypasses the root layout's title.template, which would
    // otherwise append " · MCPlexer" a second time.
    title: { absolute: fullTitle },
    description: input.description,
    alternates: {
      canonical: url,
      // Re-declare per-page so Next.js's shallow-merge of metadata
      // doesn't drop the root layout's autodiscovery link.
      types: {
        "application/atom+xml": [
          { url: `${SITE_URL}/feed.xml`, title: `${config.name} Blog` },
        ],
      },
    },
    openGraph: {
      title: fullTitle,
      description: input.description,
      url,
      type: input.type ?? "website",
      siteName: config.name,
      locale: "en_GB",
    },
    twitter: {
      card: "summary_large_image",
      title: fullTitle,
      description: input.description,
    },
    robots: {
      index: true,
      follow: true,
      googleBot: {
        index: true,
        follow: true,
        "max-snippet": -1,
        "max-image-preview": "large",
        "max-video-preview": -1,
      },
    },
    authors: input.authorName ? [{ name: input.authorName }] : undefined,
    keywords: input.tags,
  };

  if (input.type === "article") {
    meta.openGraph = {
      ...meta.openGraph,
      type: "article",
      publishedTime: input.publishedTime,
      modifiedTime: input.modifiedTime,
      authors: input.authorName ? [input.authorName] : undefined,
      tags: input.tags,
    };
  }

  return meta;
}
