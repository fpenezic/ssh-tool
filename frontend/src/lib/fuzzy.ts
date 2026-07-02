// Lightweight subsequence-based fuzzy matcher. Good enough for couple
// hundred connections; no external dep, no index build, runs in microseconds.
//
// Score: lower is better. Bonuses for: case match, start-of-token match,
// consecutive matches, prefix match. Returns null on no match.

export interface FuzzyMatch {
  score: number;
  positions: number[]; // indices in the haystack where each needle char landed
}

export function fuzzyMatch(needle: string, haystack: string): FuzzyMatch | null {
  if (!needle) return { score: 0, positions: [] };
  const n = needle.toLowerCase();
  const h = haystack.toLowerCase();

  // Greedy left-to-right walk that prefers tighter clusters.
  let needleIdx = 0;
  let lastMatchIdx = -1;
  let score = 0;
  const positions: number[] = [];

  for (let i = 0; i < h.length && needleIdx < n.length; i++) {
    if (h[i] === n[needleIdx]) {
      positions.push(i);
      // Penalties:
      //   - distance from previous match (gaps hurt)
      //   - position from start (prefer matches near start)
      // Bonuses:
      //   - case-exact match (haystack[i] === needle[needleIdx])
      //   - boundary match (preceded by space, '/', '-', '_', '.', or is i==0)
      const gap = lastMatchIdx === -1 ? 0 : i - lastMatchIdx - 1;
      score += gap * 1;
      if (i === 0) score -= 4;
      const prev = i > 0 ? h[i - 1] : "";
      const isBoundary = i === 0 || /[\s/_\-.]/.test(prev);
      if (isBoundary) score -= 2;
      if (haystack[i] === needle[needleIdx]) score -= 1;
      lastMatchIdx = i;
      needleIdx++;
    }
  }
  if (needleIdx < n.length) return null;
  // Penalize unmatched tail (favour shorter haystacks slightly).
  score += (h.length - lastMatchIdx) * 0.1;
  return { score, positions };
}

/**
 * Split a string into [matched, unmatched] runs based on position array.
 * Used to render highlighted matches in the UI.
 */
export function highlightSegments(text: string, positions: number[]): { text: string; match: boolean }[] {
  if (positions.length === 0) return [{ text, match: false }];
  const segments: { text: string; match: boolean }[] = [];
  let cursor = 0;
  for (let i = 0; i < positions.length; ) {
    const start = positions[i];
    if (cursor < start) {
      segments.push({ text: text.slice(cursor, start), match: false });
    }
    // Collect a run of consecutive position indices.
    let end = start + 1;
    let j = i + 1;
    while (j < positions.length && positions[j] === end) { end++; j++; }
    segments.push({ text: text.slice(start, end), match: true });
    cursor = end;
    i = j;
  }
  if (cursor < text.length) {
    segments.push({ text: text.slice(cursor), match: false });
  }
  return segments;
}
