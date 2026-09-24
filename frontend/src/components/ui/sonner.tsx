import { useEffect, useState, type CSSProperties } from "react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

// Follows the app's .dark class on <html>.
function useDark() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  useEffect(() => {
    const el = document.documentElement;
    const o = new MutationObserver(() => setDark(el.classList.contains("dark")));
    o.observe(el, { attributes: true, attributeFilter: ["class"] });
    return () => o.disconnect();
  }, []);
  return dark;
}

export function Toaster(props: ToasterProps) {
  const dark = useDark();
  return (
    <Sonner
      theme={dark ? "dark" : "light"}
      className="toaster group"
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius)",
        } as CSSProperties
      }
      {...props}
    />
  );
}
