import Link from "next/link";
import { config } from "@/lib/config";
import { ArrowRight } from "lucide-react";
import { McplexerLogo } from "@/components/logo";

export function Footer() {
  return (
    <footer className="border-t border-border bg-bg-alt" role="contentinfo">
      <div className="mx-auto max-w-6xl px-4 sm:px-6 py-10">
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-6">
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center gap-2">
              <McplexerLogo className="h-4 w-4 text-cyan" />
              <span className="font-bold text-text text-sm">
                {config.name}
              </span>
            </div>
            <p className="text-xs text-text-dim max-w-xs">
              {config.description}
            </p>
          </div>

          <div className="flex flex-col sm:flex-row gap-4 sm:gap-8 text-xs">
            <div className="flex flex-col gap-2">
              <span className="text-text-muted font-medium uppercase tracking-wider text-[10px]">
                Product
              </span>
              <Link
                href="/#features"
                className="text-text-dim hover:text-text transition-colors"
              >
                Features
              </Link>
              <Link
                href="/#runtime"
                className="text-text-dim hover:text-text transition-colors"
              >
                Runtime
              </Link>
              <Link
                href="/#browser-control"
                className="text-text-dim hover:text-text transition-colors"
              >
                Browser Control
              </Link>
              <Link
                href="/blog"
                className="text-text-dim hover:text-text transition-colors"
              >
                Blog
              </Link>
              <Link
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-text-dim hover:text-text transition-colors"
              >
                GitHub
              </Link>
            </div>
            <div className="flex flex-col gap-2">
              <span className="text-text-muted font-medium uppercase tracking-wider text-[10px]">
                Project
              </span>
              <Link
                href={config.githubOrgUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-text-dim hover:text-text transition-colors"
              >
                Don Works
              </Link>
              <Link
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="group flex items-center gap-1 text-cyan hover:text-cyan-light transition-colors"
              >
                Source Code
                <ArrowRight className="w-3 h-3 transition-transform group-hover:translate-x-0.5" />
              </Link>
            </div>
          </div>
        </div>

        <div className="mt-8 pt-6 border-t border-border flex flex-col sm:flex-row items-center justify-between gap-3 text-[10px] text-text-dim">
          <span>&copy; 2026 Don Works. All rights reserved.</span>
          <span>
            Built and supported by{" "}
            <Link
              href={config.githubOrgUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="text-cyan hover:text-cyan-light transition-colors"
            >
              Don Works
            </Link>
          </span>
        </div>
      </div>
    </footer>
  );
}
