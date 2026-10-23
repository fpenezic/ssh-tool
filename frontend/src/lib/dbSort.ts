// Client-side ordering of a result grid's rows: a click on a column header
// sorts what was fetched, nothing is sent to the server.

// Column types whose values compare as numbers (Postgres, MySQL,
// ClickHouse, Oracle, SQL Server and OpenSearch spellings; "number" is
// Oracle's NUMBER and a JSON number in a search hit).
const NUMERIC = /^(nullable\()?(u?int\d*|tinyint|smallint|mediumint|bigint|integer|int|serial\d*|bigserial|smallserial|numeric|decimal|float\d*|double|double precision|real|money|smallmoney|binary_float|binary_double|oid|long|short|byte|half_float|scaled_float|unsigned_long|number)\b/i;

export function isNumericType(type: string): boolean {
  return NUMERIC.test(type.trim());
}

const INT = /^-?\d+$/;

// compareNumbers orders numeric text without losing precision on integers
// past 2^53 (bigint ids): two integers compare by sign, length and digits.
export function compareNumbers(a: string, b: string): number {
  if (INT.test(a) && INT.test(b)) {
    const na = a.startsWith("-"), nb = b.startsWith("-");
    if (na !== nb) return na ? -1 : 1;
    const da = (na ? a.slice(1) : a).replace(/^0+(?=\d)/, "");
    const db = (nb ? b.slice(1) : b).replace(/^0+(?=\d)/, "");
    const c = da.length !== db.length ? da.length - db.length : da < db ? -1 : da > db ? 1 : 0;
    return na ? -c : c;
  }
  const x = Number(a), y = Number(b);
  if (Number.isNaN(x) || Number.isNaN(y)) {
    // NaN / inf text from the server: after the numbers.
    if (Number.isNaN(x) && Number.isNaN(y)) return a < b ? -1 : a > b ? 1 : 0;
    return Number.isNaN(x) ? 1 : -1;
  }
  return x - y;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

// sortedOrder returns the row indexes in the column's order. NULLs go last
// either way; ties keep the server's order.
export function sortedOrder(rows: (string | null)[][], col: number, type: string, desc: boolean): number[] {
  const num = isNumericType(type);
  const idx = rows.map((_, i) => i);
  idx.sort((i, j) => {
    const a = rows[i][col], b = rows[j][col];
    if (a === null || b === null) {
      if (a === b) return i - j;
      return a === null ? 1 : -1;
    }
    let c = num ? compareNumbers(a, b) : collator.compare(a, b);
    if (desc) c = -c;
    return c !== 0 ? c : i - j;
  });
  return idx;
}
