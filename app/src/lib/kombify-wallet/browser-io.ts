/**
 * Hand text to the person's clipboard or disk. Used for public material only
 * (an SSH CA's public key, a signed certificate); a secret is never routed
 * through here.
 */

/** True when the text reached the clipboard. */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** Offer the text as a download; true when the browser accepted it. */
export function saveTextFile(fileName: string, text: string): boolean {
  try {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = fileName;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    return true;
  } catch {
    return false;
  }
}
