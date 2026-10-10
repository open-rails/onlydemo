import { useEffect, useRef } from "react";
import { MediaFolderEditor, MediaGallery, MediaReadinessNotice } from "@openrails/contentkit-ui";
import { useMediaRead } from "@openrails/contentkit-ui/react";
import { HugeiconsIcon } from "@hugeicons/react";
import { SquareLock02Icon } from "@hugeicons/core-free-icons";
import type { Post } from "../models";
import { Button } from "@/components/ui/button";
import { postPaths, postRef } from "../media";

// What this viewer may see as a carousel or grid: every image, then every
// video, with access; otherwise the first image's blurred preview and the
// locked rest, which a purchase or membership unlocks.
export function PostGallery({ post, unlock }: { post: Post; unlock: () => void }) {
  const item = postRef(post.id);
  // Access is the app's: read again when a purchase or join opens the post.
  // Reads are kept per viewer, so a sign-in or sign-out reads again by itself.
  const { reload } = useMediaRead(item, { refresh: false });
  const access = post.can_read;
  const seen = useRef(access);
  useEffect(() => {
    if (seen.current === access) return;
    seen.current = access;
    reload();
  }, [access, reload]);
  return (
    <MediaGallery
      item={item}
      className="post-gallery"
      renderLocked={() => (
        <Button size="sm" variant="secondary" onClick={unlock}>
          <HugeiconsIcon icon={SquareLock02Icon} data-icon="inline-start" />
          Unlock
        </Button>
      )}
      label="Post media"
    />
  );
}

// A publishing post's status for its editors: it goes live by itself once
// every file is processed; a file that failed holds it back until removed.
export function PublishingNotice({ post }: { post: Post }) {
  return (
    <MediaReadinessNotice item={postRef(post.id)} className="publishing-notice" description="Only this channel's editors can see it until then." />
  );
}

// Creator tools on a post: upload images and videos, then reorder, crop,
// rename or remove them. The first image, blurred, is what readers without
// access see.
export function PostMediaEditor({ postID }: { postID: string }) {
  return (
    <MediaFolderEditor
      item={postRef(postID)}
      paths={postPaths}
      className="media-editor"
      footer={<p className="muted text-sm">Readers without access see the first image, blurred.</p>}
    />
  );
}
