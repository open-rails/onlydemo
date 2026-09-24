import { useEffect, useRef, useState } from "react";
import { auth } from "../api";

// Playlists come from the app's read API (bearer auth); byte ranges from
// media-access, signed in the playlist (URL mode) or under the folder cookie
// the playlist response sets (cookie mode, hence withCredentials).
function authorize(xhr: XMLHttpRequest, url: string) {
  xhr.open("GET", url, true);
  if (new URL(url, location.href).origin === location.origin) {
    const token = auth.getAccessToken();
    if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`);
  } else {
    xhr.withCredentials = !/[?&]t=/.test(url);
  }
}

interface Poster {
  src: string;
  x: number;
  y: number;
  w: number;
  h: number;
  cols: number;
  rows: number;
}

// The first tile of the seek sprite, as a poster until playback starts.
function usePoster(vtt: string) {
  const [poster, setPoster] = useState<Poster>();
  useEffect(() => {
    let live = true;
    void auth
      .authFetch(vtt)
      .then((r) => (r.ok ? r.text() : ""))
      .then((text) => {
        const m = text.match(/^(\S+)#xywh=(\d+),(\d+),(\d+),(\d+)$/m);
        if (!m || !live) return;
        const [x, y, w, h] = m.slice(2).map(Number);
        const img = new Image();
        img.onload = () =>
          live &&
          setPoster({ src: m[1], x, y, w, h, cols: img.naturalWidth / w, rows: img.naturalHeight / h });
        img.src = m[1];
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [vtt]);
  return poster;
}

export function VideoPlayer({ base, width, height }: { base: string; width?: number; height?: number }) {
  const video = useRef<HTMLVideoElement>(null);
  const [started, setStarted] = useState(false);
  const [error, setError] = useState("");
  const poster = usePoster(`${base}sprite.vtt`);
  useEffect(() => {
    const el = video.current;
    if (!el) return;
    const src = `${base}master.m3u8`;
    let destroy = () => {};
    let live = true;
    void import("hls.js").then(({ default: Hls }) => {
      if (!live) return;
      if (Hls.isSupported()) {
        const hls = new Hls({ xhrSetup: authorize });
        hls.on(Hls.Events.ERROR, (_, data) => {
          if (data.fatal) setError("This video can't be played right now.");
        });
        hls.loadSource(src);
        hls.attachMedia(el);
        destroy = () => hls.destroy();
        return;
      }
      // Safari without MSE plays HLS natively; it cannot send the bearer
      // token, so this path serves public posts only.
      if (el.canPlayType("application/vnd.apple.mpegurl")) el.src = src;
      else setError("This browser can't play HLS video.");
    });
    return () => {
      live = false;
      destroy();
    };
  }, [base]);
  return (
    // The source's true aspect, no taller than most of the viewport, so a
    // vertical video is a tall frame rather than a letterboxed 16:9 one.
    <div
      className="video-frame"
      style={{
        aspectRatio: width && height ? `${width} / ${height}` : "16 / 9",
        width: width && height ? `min(100%, calc(80svh * ${width / height}))` : undefined,
      }}
    >
      <video ref={video} controls playsInline preload="metadata" onPlay={() => setStarted(true)} />
      {poster && !started && (
        <div
          className="video-poster"
          aria-hidden
          style={{
            backgroundImage: `url("${poster.src}")`,
            backgroundSize: `${poster.cols * 100}% ${poster.rows * 100}%`,
            backgroundPosition: `${(poster.x / Math.max(1, poster.w * (poster.cols - 1))) * 100}% ${(poster.y / Math.max(1, poster.h * (poster.rows - 1))) * 100}%`,
          }}
          onClick={() => void video.current?.play()}
        />
      )}
      {error && <p className="video-error">{error}</p>}
    </div>
  );
}
