// copyText copies text to the clipboard, returning whether it succeeded.
// navigator.clipboard is only present in secure contexts (HTTPS); over plain
// http:// it is undefined (see web/README.md HTTPS caveat), so callers must
// handle a false return by offering a manual-select fallback.
export async function copyText(text: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) return false;
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
