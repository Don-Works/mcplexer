import type { ComponentType } from "react";

export type Post = {
  slug: string;
  title: string;
  description: string;
  excerpt: string;
  publishedAt: string;
  updatedAt?: string;
  authorName: string;
  authorUrl: string;
  tags: string[];
  /** Plain-markdown source-of-truth — served at /blog/[slug].md and aggregated into /llms-full.txt. */
  markdown: string;
  /** Rendered React body. */
  Body: ComponentType;
  /** Approx reading time, populated from markdown. */
  readingTimeMinutes: number;
};

const allPosts: Post[] = [];

export function listPosts(): Post[] {
  return [...allPosts].sort((a, b) =>
    b.publishedAt.localeCompare(a.publishedAt),
  );
}

export function getPost(slug: string): Post | undefined {
  return allPosts.find((p) => p.slug === slug);
}

export function postUrl(slug: string): string {
  return `https://mcplexer.com/blog/${slug}`;
}

export function computeReadingTime(markdown: string): number {
  const words = markdown.trim().split(/\s+/).length;
  return Math.max(1, Math.round(words / 220));
}
