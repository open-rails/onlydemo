import { toast } from "sonner";
import { createTranslator, resolveMessages } from "@openrails/contentkit-ui";
import type { ContentKitOperation as UploadUiOperation } from "@openrails/contentkit-ui/react";

// Interim, for the composer's draft media (English, the app's language).
const text = createTranslator(resolveMessages());

/** A media failure in words: a refusal states its rule, a fault asks to retry. */
export const mediaMessage = (error: unknown) => text.error(error);

const titles: Partial<Record<UploadUiOperation, string>> = {
  "poster.load": "Couldn't load the cover",
  "poster.frame": "Couldn't load that frame",
  "poster.save": "Couldn't set the cover",
  "slot.load": "Couldn't load the image",
  "slot.decode": "Couldn't open the image",
  "slot.save": "Couldn't save the image",
  "slot.remove": "Couldn't remove the image",
  upload: "Upload failed",
};

/** Toasts a media failure; `title` is an operation or the host's own words. */
export function toastMediaError(error: unknown, title: UploadUiOperation | string) {
  toast.error(titles[title as UploadUiOperation] ?? title, { description: mediaMessage(error) });
}
