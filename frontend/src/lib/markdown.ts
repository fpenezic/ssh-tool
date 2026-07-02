// Minimal Markdown → HTML renderer for connection notes.
//
// Intentionally small: headings, bold, italic, inline code, fenced
// code, unordered lists, ordered lists, hr, links, paragraphs. No
// tables, no blockquotes, no images, no HTML passthrough. Inputs
// are HTML-escaped first so the only tags in the output are the
// ones this function emits.
//
// Why hand-rolled and not `marked`: notes are small, dep-free is
// nice, and the bar is just "this looks like a runbook now". If we
// ever need tables / GFM, swap in marked behind this function.

const ESC: Record<string, string> = {
  "&": "&amp;",
  "<": "&lt;",
  ">": "&gt;",
  "\"": "&quot;",
  "'": "&#39;",
};

function escape(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ESC[c]);
}

// Inline pass: applies to already-escaped text. Order matters -
// code spans first so their contents don't get further mangled.
function inline(s: string): string {
  // Inline code: `foo`
  s = s.replace(/`([^`\n]+)`/g, (_, body) => `<code>${body}</code>`);
  // Links: [text](url) - http(s) only, no javascript: pseudo.
  s = s.replace(
    /\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
    (_, text, url) =>
      `<a href="${url}" class="md-link" data-md-link="1">${text}</a>`,
  );
  // Bold: **foo** or __foo__
  s = s.replace(/(\*\*|__)([^*_\n][^*_\n]*?)\1/g, "<strong>$2</strong>");
  // Italic: *foo* or _foo_  (avoid bold leftovers - those got eaten above)
  s = s.replace(/(\*|_)([^*_\n][^*_\n]*?)\1/g, "<em>$2</em>");
  return s;
}

export function renderMarkdown(src: string): string {
  if (!src) return "";
  // Normalise line endings and escape the whole thing first.
  const lines = escape(src.replace(/\r\n/g, "\n")).split("\n");

  const out: string[] = [];
  let i = 0;

  function flushParagraph(buf: string[]) {
    if (buf.length === 0) return;
    out.push(`<p>${inline(buf.join(" ").trim())}</p>`);
    buf.length = 0;
  }

  let paragraph: string[] = [];

  while (i < lines.length) {
    const line = lines[i];

    // Blank line ends a paragraph.
    if (line.trim() === "") {
      flushParagraph(paragraph);
      i++;
      continue;
    }

    // Fenced code block: ```lang ... ```
    if (/^```/.test(line)) {
      flushParagraph(paragraph);
      const fence = line.match(/^```\s*(\S*)\s*$/);
      const lang = fence?.[1] ?? "";
      const buf: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        buf.push(lines[i]);
        i++;
      }
      // Skip the closing fence (if any).
      if (i < lines.length) i++;
      const cls = lang ? ` class="lang-${escape(lang)}"` : "";
      out.push(`<pre><code${cls}>${buf.join("\n")}</code></pre>`);
      continue;
    }

    // Horizontal rule: ---  (or *** / ___)
    if (/^(\s*)(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      flushParagraph(paragraph);
      out.push("<hr>");
      i++;
      continue;
    }

    // Heading: # to ######
    const h = line.match(/^(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (h) {
      flushParagraph(paragraph);
      const level = h[1].length;
      out.push(`<h${level}>${inline(h[2])}</h${level}>`);
      i++;
      continue;
    }

    // Unordered list
    if (/^\s*[-*+]\s+/.test(line)) {
      flushParagraph(paragraph);
      const items: string[] = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*[-*+]\s+/, ""));
        i++;
      }
      out.push(`<ul>${items.map((it) => `<li>${inline(it)}</li>`).join("")}</ul>`);
      continue;
    }

    // Ordered list
    if (/^\s*\d+\.\s+/.test(line)) {
      flushParagraph(paragraph);
      const items: string[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*\d+\.\s+/, ""));
        i++;
      }
      out.push(`<ol>${items.map((it) => `<li>${inline(it)}</li>`).join("")}</ol>`);
      continue;
    }

    // Default: paragraph line.
    paragraph.push(line);
    i++;
  }

  flushParagraph(paragraph);
  return out.join("\n");
}
