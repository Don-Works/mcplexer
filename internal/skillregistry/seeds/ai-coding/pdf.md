---
name: pdf
description: Convert markdown files to styled PDFs using md-to-pdf
---

# Convert Markdown to PDF

Convert markdown files to styled PDFs using md-to-pdf.

## Usage

```
/pdf path/to/file.md              # Single file
/pdf path/to/directory/            # All .md files in directory
/pdf path/to/file.md --style dark  # With dark theme
```

## Input

**Target:** `$ARGUMENTS`

If the argument is a file, convert that file. If it's a directory, convert all `.md` files in it. If no argument is given, ask the user what to convert.

## Execution

### Step 1 — Locate or Create Stylesheet

Check for a stylesheet in this order:
1. `config/pdf-style.css` in the current project
2. `~/.claude/pdf-style.css` (global default)

If neither exists, create `~/.claude/pdf-style.css` with this default style:

```css
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap');

:root {
  --primary: #2563eb;
  --primary-light: #eff6ff;
  --primary-dark: #1d4ed8;
  --text: #1a1a2e;
  --text-muted: #4a4a6a;
  --border: #e2e8f0;
  --bg-subtle: #f8fafc;
}

@page {
  size: A4;
  margin: 25mm 20mm 25mm 20mm;
}

body {
  font-family: 'Inter', -apple-system, sans-serif;
  font-size: 10pt;
  line-height: 1.6;
  color: var(--text);
}

h1 {
  font-size: 26pt;
  font-weight: 700;
  color: var(--primary);
  border-bottom: 3px solid var(--primary);
  padding-bottom: 12px;
  margin-bottom: 24px;
}

h2 {
  font-size: 16pt;
  font-weight: 700;
  border-bottom: 1px solid var(--border);
  padding-bottom: 6px;
  margin-top: 32px;
  page-break-after: avoid;
}

h3 {
  font-size: 12pt;
  font-weight: 600;
  color: var(--primary-dark);
  margin-top: 20px;
  page-break-after: avoid;
}

table {
  width: 100%;
  border-collapse: collapse;
  margin: 12px 0 20px 0;
  font-size: 9pt;
}

thead {
  display: table-header-group;
  break-after: avoid;
  page-break-after: avoid;
}

tbody tr:nth-child(-n+2) {
  break-before: avoid;
  page-break-before: avoid;
}

tr {
  page-break-inside: avoid;
}

th {
  background: var(--primary);
  color: white;
  font-weight: 600;
  text-align: left;
  padding: 8px 10px;
  font-size: 8.5pt;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

td {
  padding: 7px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

tr:nth-child(even) td {
  background: var(--bg-subtle);
}

blockquote {
  border-left: 4px solid var(--primary);
  background: var(--primary-light);
  margin: 16px 0;
  padding: 12px 16px;
  border-radius: 0 6px 6px 0;
}

code {
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 3px;
  padding: 1px 4px;
  font-size: 9pt;
  color: var(--primary-dark);
}

pre {
  background: #1e1e2e;
  color: #cdd6f4;
  padding: 14px 16px;
  border-radius: 6px;
  font-size: 8.5pt;
  page-break-inside: avoid;
}

pre code {
  background: none;
  border: none;
  padding: 0;
  color: inherit;
}

hr {
  border: none;
  border-top: 2px solid var(--border);
  margin: 28px 0;
}

a { color: var(--primary); text-decoration: none; }

@media print {
  body { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
}
```

### Step 2 — Convert

For each markdown file, run:

```bash
npx --yes md-to-pdf "{file}" \
  --stylesheet "{stylesheet}" \
  --launch-options '{"headless": "new"}' \
  --pdf-options '{"format": "A4", "margin": {"top": "25mm", "right": "20mm", "bottom": "25mm", "left": "20mm"}, "printBackground": true}'
```

This creates a `.pdf` file next to each `.md` file.

### Step 3 — Organise (optional)

If the user specified `--output {dir}`, move the generated PDFs to that directory.

### Step 4 — Report

```
Generated PDFs:
  - file-one.pdf (245 KB)
  - file-two.pdf (312 KB)
  ...
```

## Prerequisites

If `npx md-to-pdf` is not available, tell the user:
```
npm install -g md-to-pdf
```

## Notes

- The PDF is generated using Puppeteer (headless Chrome), so the output matches what you'd see in a browser
- Project stylesheets in `config/pdf-style.css` override the global default — use this for branded colours and footers
- For the best results, keep tables narrow and use short sentences
