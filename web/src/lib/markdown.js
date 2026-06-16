// Minimal, XSS-safe markdown renderer.
// HTML is escaped first; only our own tags are ever injected.

function escapeHTML(str) {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

export function renderMarkdown(src) {
  if (!src) return '';

  // Step 1: escape all HTML
  let s = escapeHTML(src);

  // Step 2: fenced code blocks (``` ... ```)
  s = s.replace(/```(\w*)\n?([\s\S]*?)```/g, (_, lang, code) => {
    return `<pre><code>${code}</code></pre>`;
  });

  // Step 3: process line by line for headings, lists, paragraphs
  const lines = s.split('\n');
  const out = [];
  let inList = false;
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Skip lines already inside a <pre> block (already replaced above)
    if (line.startsWith('<pre>')) {
      if (inList) { out.push('</ul>'); inList = false; }
      out.push(line);
      i++;
      continue;
    }

    // Headings
    const h = line.match(/^(#{1,6})\s+(.*)/);
    if (h) {
      if (inList) { out.push('</ul>'); inList = false; }
      const level = h[1].length;
      out.push(`<h${level}>${inlineMarkdown(h[2])}</h${level}>`);
      i++;
      continue;
    }

    // Unordered list items (- or *)
    const li = line.match(/^[-*]\s+(.*)/);
    if (li) {
      if (!inList) { out.push('<ul>'); inList = true; }
      out.push(`<li>${inlineMarkdown(li[1])}</li>`);
      i++;
      continue;
    }

    // Close list if open and line is not a list item
    if (inList) { out.push('</ul>'); inList = false; }

    // Blank line = paragraph separator
    if (line.trim() === '') {
      out.push('<br>');
      i++;
      continue;
    }

    // Regular paragraph line
    out.push(`<p>${inlineMarkdown(line)}</p>`);
    i++;
  }

  if (inList) out.push('</ul>');

  return out.join('\n');
}

// Inline transforms: bold, italic, inline code, links
function inlineMarkdown(s) {
  // Inline code (must go before bold/italic to protect content)
  s = s.replace(/`([^`]+)`/g, '<code>$1</code>');

  // Bold **text**
  s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');

  // Italic *text* (not double-star)
  s = s.replace(/(?<!\*)\*(?!\*)([^*]+)(?<!\*)\*(?!\*)/g, '<em>$1</em>');

  // Links [text](url) — only http/https allowed
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, text, url) => {
    if (/^https?:\/\//i.test(url)) {
      return `<a href="${url}" target="_blank" rel="noopener noreferrer">${text}</a>`;
    }
    // unsafe scheme: render as plain text
    return `${text} (${url})`;
  });

  return s;
}
