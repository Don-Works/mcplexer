"use client";

import { useState } from "react";
import Link from "next/link";
import { config } from "@/lib/config";
import { Menu, X, ArrowRight } from "lucide-react";
import { McplexerLogo } from "@/components/logo";

const navLinks = [
  { href: "/#features", label: "Features" },
  { href: "/#runtime", label: "Runtime" },
  { href: "/#browser-control", label: "Browser" },
  { href: "/#servers", label: "Servers" },
  { href: "/blog", label: "Blog" },
];

export function Header() {
  const [mobileOpen, setMobileOpen] = useState(false);

  return (
    <header className="fixed top-0 z-50 w-full border-b border-border bg-bg/80 backdrop-blur-md" role="banner">
      <div className="mx-auto flex h-14 max-w-6xl items-center justify-between px-4 sm:px-6">
        <Link href="/" className="flex items-center gap-2.5 group" aria-label={`${config.name} home`}>
          <span className="relative flex items-center justify-center">
            <McplexerLogo className="relative z-10 h-5 w-5 text-cyan" />
            <span className="absolute inset-0 bg-cyan/20 blur-md group-hover:bg-cyan/30 transition-colors" />
          </span>
          <span className="font-bold text-text text-sm uppercase">
            MCPLEXER
          </span>
        </Link>

        {/* Desktop nav */}
        <nav className="hidden sm:flex items-center gap-1" aria-label="Primary">
          {navLinks.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-text-muted hover:text-text transition-colors"
            >
              {link.label}
            </Link>
          ))}
          <Link
            href={config.githubUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="ml-2 group flex items-center gap-1.5 px-3 py-1.5 text-xs bg-cyan/10 text-cyan border border-cyan/20 hover:bg-cyan/20 transition-colors"
          >
            GitHub
            <ArrowRight className="w-3 h-3 transition-transform group-hover:translate-x-0.5" />
          </Link>
        </nav>

        {/* Mobile toggle */}
        <button
          onClick={() => setMobileOpen(!mobileOpen)}
          className="sm:hidden p-1.5 text-text-muted hover:text-text transition-colors"
          aria-label="Toggle menu"
        >
          {mobileOpen ? (
            <X className="w-5 h-5" />
          ) : (
            <Menu className="w-5 h-5" />
          )}
        </button>
      </div>

      {/* Mobile nav */}
      {mobileOpen && (
        <nav className="sm:hidden border-t border-border bg-bg/95 backdrop-blur-md" aria-label="Mobile">
          <div className="flex flex-col px-4 py-3 gap-1">
            {navLinks.map((link) => (
              <Link
                key={link.href}
                href={link.href}
                onClick={() => setMobileOpen(false)}
                className="flex items-center gap-2 px-3 py-2 text-sm text-text-muted hover:text-text transition-colors"
              >
                {link.label}
              </Link>
            ))}
            <Link
              href={config.githubUrl}
              target="_blank"
              rel="noopener noreferrer"
              onClick={() => setMobileOpen(false)}
              className="mt-1 px-3 py-2 text-sm bg-cyan/10 text-cyan border border-cyan/20 text-center"
            >
              GitHub
            </Link>
          </div>
        </nav>
      )}
    </header>
  );
}
