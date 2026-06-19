import Link from "next/link";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { ArrowRight, ArrowLeft, Calendar, Clock, FileCode } from "lucide-react";
import { listPosts, getPost } from "@/lib/posts";
import { pageMetadata, SITE_URL } from "@/lib/seo";
import { JsonLd } from "@/components/json-ld";
import { config } from "@/lib/config";

type Params = Promise<{ slug: string }>;

export function generateStaticParams() {
  return listPosts().map((p) => ({ slug: p.slug }));
}

export async function generateMetadata({
  params,
}: {
  params: Params;
}): Promise<Metadata> {
  const { slug } = await params;
  const post = getPost(slug);
  if (!post) return {};
  return pageMetadata({
    title: post.title,
    description: post.description,
    path: `/blog/${post.slug}`,
    type: "article",
    publishedTime: post.publishedAt,
    modifiedTime: post.updatedAt,
    authorName: post.authorName,
    tags: post.tags,
  });
}

export default async function PostPage({ params }: { params: Params }) {
  const { slug } = await params;
  const post = getPost(slug);
  if (!post) notFound();

  const PostBody = post.Body;
  const url = `${SITE_URL}/blog/${post.slug}`;

  const articleJsonLd = {
    "@context": "https://schema.org",
    "@type": "BlogPosting",
    headline: post.title,
    description: post.description,
    url,
    mainEntityOfPage: { "@type": "WebPage", "@id": url },
    datePublished: post.publishedAt,
    dateModified: post.updatedAt ?? post.publishedAt,
    keywords: post.tags.join(", "),
    inLanguage: "en-GB",
    author: {
      "@type": "Person",
      name: post.authorName,
      url: post.authorUrl,
    },
    publisher: {
      "@type": "Organization",
      name: config.name,
      url: SITE_URL,
      logo: {
        "@type": "ImageObject",
        url: `${SITE_URL}/icon.svg`,
      },
    },
    articleSection: "Security",
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
      {
        "@type": "ListItem",
        position: 3,
        name: post.title,
        item: url,
      },
    ],
  };

  return (
    <>
      <JsonLd data={[articleJsonLd, breadcrumbJsonLd]} />

      <article
        className="mx-auto max-w-3xl px-4 sm:px-6 pt-32 pb-20"
        itemScope
        itemType="https://schema.org/BlogPosting"
      >
        <link itemProp="mainEntityOfPage" href={url} />

        <header className="mb-12">
          <p className="text-xs font-mono uppercase tracking-wider text-text-muted mb-6">
            <Link
              href="/"
              className="hover:text-cyan transition-colors"
            >
              Home
            </Link>{" "}
            /{" "}
            <Link
              href="/blog"
              className="hover:text-cyan transition-colors"
            >
              Blog
            </Link>{" "}
            / <span className="text-text">{post.title}</span>
          </p>

          <h1
            className="text-3xl sm:text-4xl font-bold tracking-tight mb-5 leading-tight"
            itemProp="headline"
          >
            {post.title}
          </h1>

          <p className="text-base text-text-muted leading-relaxed mb-6 max-w-2xl">
            {post.description}
          </p>

          <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs font-mono uppercase tracking-wider text-text-muted">
            <span className="inline-flex items-center gap-1.5">
              <Calendar className="w-3 h-3" />
              <time dateTime={post.publishedAt} itemProp="datePublished">
                {formatDate(post.publishedAt)}
              </time>
              {post.updatedAt && post.updatedAt !== post.publishedAt && (
                <>
                  <span aria-hidden> · </span>
                  <span>updated</span>
                  <time dateTime={post.updatedAt} itemProp="dateModified">
                    {formatDate(post.updatedAt)}
                  </time>
                </>
              )}
            </span>
            <span className="inline-flex items-center gap-1.5">
              <Clock className="w-3 h-3" />
              {post.readingTimeMinutes} min read
            </span>
            <span
              itemScope
              itemProp="author"
              itemType="https://schema.org/Person"
              className="inline-flex items-center gap-1.5"
            >
              by{" "}
              <a
                href={post.authorUrl}
                rel="author"
                target="_blank"
                className="text-cyan hover:text-cyan-light transition-colors"
                itemProp="url"
              >
                <span itemProp="name">{post.authorName}</span>
              </a>
            </span>
            <Link
              href={`/blog/${post.slug}/raw.md`}
              className="inline-flex items-center gap-1.5 text-cyan hover:text-cyan-light transition-colors"
              title="Plain-text markdown source for LLMs"
            >
              <FileCode className="w-3 h-3" />
              raw.md
            </Link>
          </div>
        </header>

        <div className="post-prose" itemProp="articleBody">
          <PostBody />
        </div>

        <footer className="mt-16 pt-8 border-t border-border flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
          <Link
            href="/blog"
            className="inline-flex items-center gap-1.5 text-xs text-text-muted hover:text-cyan transition-colors"
          >
            <ArrowLeft className="w-3 h-3" />
            All posts
          </Link>
          <Link
            href={config.githubUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="group inline-flex items-center gap-1.5 px-4 py-2 text-xs bg-cyan/10 text-cyan border border-cyan/20 hover:bg-cyan/20 transition-colors"
          >
            View source
            <ArrowRight className="w-3 h-3 transition-transform group-hover:translate-x-0.5" />
          </Link>
        </footer>
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
