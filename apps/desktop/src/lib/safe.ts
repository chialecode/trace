/**
 * Text that came from outside (file names, titles, tags) is shown as plain text only. Vue
 * escapes markup; this also makes control characters and text-direction overrides visible
 * instead of letting them hide the real end of a name.
 */

// C0 and C1 controls, left-to-right/right-to-left marks, embeddings, overrides and isolates.
const UNSAFE = /[\u0000-\u001f\u007f-\u009f\u200e\u200f\u202a-\u202e\u2066-\u2069]/g;
const REPLACEMENT = "\ufffd";

/** The name with every control or bidi-override character replaced by U+FFFD. */
export function displayText(text: string | null | undefined): string {
  return (text ?? "").replace(UNSAFE, REPLACEMENT);
}

/** Whether `displayText` had to change something (to show a "contains hidden characters" cue). */
export function hasHiddenCharacters(text: string | null | undefined): boolean {
  return displayText(text) !== (text ?? "");
}

/** The last path segment of a path with `\` or `/` separators. */
export function leafName(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) ?? path;
}
