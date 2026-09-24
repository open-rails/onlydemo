import { toast } from "sonner";
import { createTranslator, resolveMessages, type UploadUiOperation } from "@openrails/contentkit-upload/ui";
import { de } from "@openrails/contentkit-upload/locales/de";
import { en } from "@openrails/contentkit-upload/locales/en";
import { es } from "@openrails/contentkit-upload/locales/es";
import { ja } from "@openrails/contentkit-upload/locales/ja";
import { ko } from "@openrails/contentkit-upload/locales/ko";
import { zh } from "@openrails/contentkit-upload/locales/zh";

// ContentKit's upload UI follows the page's shadcn tokens, .dark class and <html lang>.
const uploadLocales = { de, en, es, ja, ko, zh };
export const uploadMessages =
  uploadLocales[document.documentElement.lang.slice(0, 2) as keyof typeof uploadLocales] ?? en;
const text = createTranslator(resolveMessages(uploadMessages));

/** A media failure in words: a refusal states its rule, a fault asks to retry. */
export const mediaMessage = (error: unknown) => text.error(error);

const titles: Record<UploadUiOperation, string> = {
  "poster.load": "Couldn't load the cover",
  "poster.frame": "Couldn't load that frame",
  "poster.save": "Couldn't set the cover",
  "preview.load": "Couldn't load the hover preview",
  "preview.frame": "Couldn't load the video frames",
  "preview.save": "Couldn't set the hover preview",
  "slot.load": "Couldn't load the image",
  "slot.decode": "Couldn't open the image",
  "slot.save": "Couldn't save the image",
  upload: "Upload failed",
};

/** Toasts a media failure; `title` is an operation or the host's own words. */
export function toastMediaError(error: unknown, title: UploadUiOperation | string) {
  toast.error(titles[title as UploadUiOperation] ?? title, { description: mediaMessage(error) });
}
