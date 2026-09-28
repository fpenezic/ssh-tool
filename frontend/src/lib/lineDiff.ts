// Line diff for the Compare view: longest common subsequence over lines,
// emitted as aligned rows for a side-by-side table. Config files are small;
// past DIFF_MAX_CELLS the table would cost too much memory, and the caller
// gets null and says so instead of freezing the window.

export type DiffRow =
  | { kind: "same"; left: string; right: string; ln: number; rn: number }
  | { kind: "del"; left: string; ln: number }
  | { kind: "add"; right: string; rn: number }
  | { kind: "chg"; left: string; right: string; ln: number; rn: number };

export const DIFF_MAX_CELLS = 6_000_000;

export function splitLines(s: string): string[] {
  const lines = s.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

export function lineDiff(a: string[], b: string[], ignoreWs = false): DiffRow[] | null {
  const norm = (x: string) => (ignoreWs ? x.replace(/\s+/g, " ").trim() : x);
  // Trim the common head and tail first: most config diffs are a few
  // lines in the middle of an identical file.
  let head = 0;
  while (head < a.length && head < b.length && norm(a[head]) === norm(b[head])) head++;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head &&
         norm(a[a.length - 1 - tail]) === norm(b[b.length - 1 - tail])) tail++;
  const A = a.slice(head, a.length - tail);
  const B = b.slice(head, b.length - tail);
  const n = A.length, m = B.length;
  if ((n + 1) * (m + 1) > DIFF_MAX_CELLS) return null;

  // dp[i][j] = LCS length of A[i..], B[j..], in one flat typed array.
  const w = m + 1;
  const dp = new Uint32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i * w + j] = norm(A[i]) === norm(B[j])
        ? dp[(i + 1) * w + j + 1] + 1
        : Math.max(dp[(i + 1) * w + j], dp[i * w + j + 1]);
    }
  }

  const rows: DiffRow[] = [];
  for (let k = 0; k < head; k++) rows.push({ kind: "same", left: a[k], right: b[k], ln: k + 1, rn: k + 1 });
  let i = 0, j = 0;
  const dels: { t: string; ln: number }[] = [];
  const adds: { t: string; rn: number }[] = [];
  const flush = () => {
    // Pair up a run of removals with the additions that replaced them, so
    // a changed line sits opposite its new version.
    const pairs = Math.min(dels.length, adds.length);
    for (let k = 0; k < pairs; k++) rows.push({ kind: "chg", left: dels[k].t, right: adds[k].t, ln: dels[k].ln, rn: adds[k].rn });
    for (let k = pairs; k < dels.length; k++) rows.push({ kind: "del", left: dels[k].t, ln: dels[k].ln });
    for (let k = pairs; k < adds.length; k++) rows.push({ kind: "add", right: adds[k].t, rn: adds[k].rn });
    dels.length = 0;
    adds.length = 0;
  };
  while (i < n || j < m) {
    if (i < n && j < m && norm(A[i]) === norm(B[j])) {
      flush();
      rows.push({ kind: "same", left: A[i], right: B[j], ln: head + i + 1, rn: head + j + 1 });
      i++; j++;
    } else if (j < m && (i >= n || dp[i * w + j + 1] >= dp[(i + 1) * w + j])) {
      adds.push({ t: B[j], rn: head + j + 1 });
      j++;
    } else {
      dels.push({ t: A[i], ln: head + i + 1 });
      i++;
    }
  }
  flush();
  for (let k = 0; k < tail; k++) {
    const li = a.length - tail + k, ri = b.length - tail + k;
    rows.push({ kind: "same", left: a[li], right: b[ri], ln: li + 1, rn: ri + 1 });
  }
  return rows;
}
