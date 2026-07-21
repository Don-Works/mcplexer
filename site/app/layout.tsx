import type { Metadata } from "next";
import { JetBrains_Mono } from "next/font/google";
import { Analytics } from "@vercel/analytics/next";
import { config } from "@/lib/config";
import { Header } from "@/components/header";
import { Footer } from "@/components/footer";
import { JsonLd } from "@/components/json-ld";
import { SITE_URL } from "@/lib/seo";
import "./globals.css";

const jetbrainsMono = JetBrains_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: {
    default: `${config.name} — ${config.tagline}`,
    template: `%s · ${config.name}`,
  },
  description: config.description,
  metadataBase: new URL(SITE_URL),
  alternates: {
    canonical: SITE_URL,
    types: {
      "application/atom+xml": [
        { url: `${SITE_URL}/feed.xml`, title: `${config.name} Blog` },
      ],
    },
  },
  openGraph: {
    title: `${config.name} — ${config.tagline}`,
    description: config.description,
    type: "website",
    url: SITE_URL,
    siteName: config.name,
    locale: "en_GB",
  },
  twitter: {
    card: "summary_large_image",
    title: `${config.name} — ${config.tagline}`,
    description: config.description,
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
  icons: {
    icon: "/icon.svg",
  },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // Site-wide JSON-LD: an Organization (Don Works) and a WebSite. Per-page
  // JSON-LD (BlogPosting, BreadcrumbList, SoftwareApplication, FAQPage) is
  // injected by the individual page components.
  const orgJsonLd = {
    "@context": "https://schema.org",
    "@type": "Organization",
    name: "Don Works",
    url: config.githubOrgUrl,
    logo: `${SITE_URL}/icon.svg`,
    sameAs: [
      config.githubOrgUrl,
      config.githubUrl,
    ],
  };

  const websiteJsonLd = {
    "@context": "https://schema.org",
    "@type": "WebSite",
    name: config.name,
    url: SITE_URL,
    description: config.description,
    publisher: {
      "@type": "Organization",
      name: "Don Works",
      url: config.githubOrgUrl,
    },
    inLanguage: "en-GB",
  };

  return (
    <html lang="en" className={jetbrainsMono.variable}>
      <head>
        <JsonLd data={[orgJsonLd, websiteJsonLd]} />
      </head>
      <body className="font-mono antialiased">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-[60] focus:bg-cyan focus:text-bg focus:px-4 focus:py-2 focus:text-sm"
        >
          Skip to main content
        </a>
        <Header />
        <main id="main">{children}</main>
        <Footer />
        <Analytics />
      </body>
    </html>
  );
}
