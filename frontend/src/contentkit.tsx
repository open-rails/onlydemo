import { useCallback, useSyncExternalStore, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { sessionIdentity, sessionUser } from "@openrails/auth-ui/react";
import { ContentKitUiProvider, defineMessages, useMessages } from "@openrails/contentkit-ui";
import { ContentKitProvider, type ContentKitErrorHandler, type ContentKitOperation, type Navigate } from "@openrails/contentkit-ui/react";
import type { ContentKitChange } from "@openrails/contentkit-ui/client";
import { auth } from "./api";
import { contentkit } from "./media";
import { contentURLs } from "./paths";
import { purchaseCount, subscribePurchases } from "./purchases";
import { openSignIn } from "./session";

// The kit follows the page's shadcn tokens, .dark class and <html lang>.
const language = document.documentElement.lang;
const messages = defineMessages({
  folder: { commit: "Add {count} to post" },
  readiness: { processing: "Publishes when processing finishes", failedHint: "Remove them and the post publishes by itself." },
});

const titles: Partial<Record<ContentKitOperation, string>> = {
  upload: "Upload failed",
  "folder.load": "Couldn't load the media",
  "folder.commit": "Couldn't add the media to the post",
  "folder.update": "Couldn't update the media",
  "folder.process": "Couldn't process a file",
  "crop.load": "Couldn't load the image",
  "crop.save": "Couldn't save the crop",
  "poster.load": "Couldn't load the cover",
  "poster.frame": "Couldn't load that frame",
  "poster.save": "Couldn't set the cover",
  "slot.load": "Couldn't load the image",
  "slot.decode": "Couldn't open the image",
  "slot.save": "Couldn't save the image",
  "slot.remove": "Couldn't remove the image",
  "notifications.load": "Couldn't load notifications",
  "notifications.read": "Couldn't update the notification",
  "notifications.dismiss": "Couldn't archive the notification",
};

/** ContentKit's client, URLs and look for the whole app; media failures are toasted. */
export function ContentKitHost({ navigate, children }: { navigate: Navigate; children: ReactNode }) {
  return (
    <ContentKitUiProvider appearance={{ theme: "inherit" }} language={language} messages={messages}>
      <Provider navigate={navigate}>{children}</Provider>
    </ContentKitUiProvider>
  );
}

function Provider({ navigate, children }: { navigate: Navigate; children: ReactNode }) {
  const client = useQueryClient();
  const session = useSyncExternalStore(auth.subscribe, auth.getSnapshot);
  const viewer = sessionUser(session);
  const purchased = useSyncExternalStore(subscribePurchases, purchaseCount);
  const { error: text } = useMessages();
  const onError = useCallback<ContentKitErrorHandler>(
    (error, { operation, file }) => {
      const title = titles[operation] ?? "Something went wrong";
      toast.error(file ? `${title}: ${file}` : title, { description: text(error) });
    },
    [text],
  );
  // A purchase, join or edit changes what the app's queries show.
  const onChange = useCallback(
    (change: ContentKitChange) => {
      if (change.type === "access.changed" || change.type === "groups.changed" || change.type === "content.changed") void client.invalidateQueries();
    },
    [client],
  );
  return (
    <ContentKitProvider
      client={contentkit}
      viewer={viewer}
      accessRevision={`${sessionIdentity(session)}:${purchased}`}
      onSignIn={openSignIn}
      urls={contentURLs}
      navigate={navigate}
      onChange={onChange}
      onError={onError}
    >
      {children}
    </ContentKitProvider>
  );
}
