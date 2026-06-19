import { listPosts, getPost } from "@/lib/posts";

type Params = Promise<{ slug: string }>;

export const dynamic = "force-static";

export function generateStaticParams() {
  return listPosts().map((p) => ({ slug: p.slug }));
}

export async function GET(_req: Request, { params }: { params: Params }) {
  const { slug } = await params;
  const post = getPost(slug);
  if (!post) {
    return new Response("post not found", { status: 404 });
  }
  return new Response(post.markdown, {
    status: 200,
    headers: {
      "Content-Type": "text/markdown; charset=utf-8",
      "Cache-Control": "public, max-age=300, must-revalidate",
      "X-Content-Type-Options": "nosniff",
    },
  });
}
