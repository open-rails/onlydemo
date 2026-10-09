import { useEffect } from "react";

// A page's canonical URL and title in the head, for link previews and
// crawlers. The server sends the same tags with canonical pages.
function tag(selector: string, create: () => HTMLElement) {
  return document.head.querySelector<HTMLElement>(selector) ?? document.head.appendChild(create());
}

export function useCanonical(path: string | undefined, title?: string) {
  useEffect(() => {
    if (!path) return;
    const url = location.origin + path;
    const link = tag('link[rel="canonical"]', () => Object.assign(document.createElement("link"), { rel: "canonical" }));
    link.setAttribute("href", url);
    const og = tag('meta[property="og:url"]', () => {
      const m = document.createElement("meta");
      m.setAttribute("property", "og:url");
      return m;
    });
    og.setAttribute("content", url);
    const ogTitle = title
      ? tag('meta[property="og:title"]', () => {
          const m = document.createElement("meta");
          m.setAttribute("property", "og:title");
          return m;
        })
      : null;
    ogTitle?.setAttribute("content", title!);
    return () => {
      link.remove();
      og.remove();
      ogTitle?.remove();
    };
  }, [path, title]);
}
