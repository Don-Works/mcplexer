import type { ReactElement } from "react";

/**
 * JsonLd renders a Schema.org JSON-LD block as a script tag. Next.js sanitises
 * dangerouslySetInnerHTML for actual HTML, but JSON content is safe because
 * we control the input. We escape `<` to defeat any malicious string slipping
 * into a property value.
 */
export function JsonLd({ data }: { data: object | object[] }): ReactElement {
  const json = JSON.stringify(data).replace(/</g, "\\u003c");
  return (
    <script
      type="application/ld+json"
      dangerouslySetInnerHTML={{ __html: json }}
    />
  );
}
