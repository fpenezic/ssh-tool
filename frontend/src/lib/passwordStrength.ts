// Lightweight password strength estimator.
//
// Not a real zxcvbn - pure-JS zxcvbn is ~700 KB minified, way too
// fat for a desktop app where this is a hint, not a security gate.
// What we do instead: estimate entropy from length × character-class
// diversity, then penalize obvious patterns (repeats, sequences,
// dictionary hits). Output is a 0-4 score matching zxcvbn's
// convention so the UI bar reads the same.
//
// Score meaning:
//   0 - very weak (≤ ~28 bits)
//   1 - weak     (~28..40)
//   2 - fair     (~40..52)
//   3 - strong   (~52..64)
//   4 - very strong (> 64)

export interface PasswordStrength {
  score: 0 | 1 | 2 | 3 | 4;
  entropy: number;       // estimated bits
  label: string;         // human readable
  feedback: string[];    // short suggestions / warnings
}

// Tiny dictionary of common passwords / words. Real zxcvbn ships
// hundreds of thousands of entries; we cover the top hits so
// "password", "qwerty", "admin" etc don't sneak past as long.
const COMMON_WORDS = new Set([
  "password", "passw0rd", "passwort", "qwerty", "qwertz", "asdf",
  "asdfgh", "admin", "administrator", "welcome", "login", "letmein",
  "root", "toor", "test", "demo", "guest", "monkey", "dragon",
  "iloveyou", "sunshine", "princess", "football", "baseball",
  "abc", "abc123", "123456", "12345678", "111111", "000000",
  "qaz", "wsx", "secret", "master", "manager", "p@ssword",
  "p@ssw0rd", "changeme", "default", "internet", "linux", "google",
  "facebook", "windows", "michael", "jennifer", "thomas", "robert",
]);

const SEQUENCES = [
  "abcdefghijklmnopqrstuvwxyz",
  "qwertyuiopasdfghjklzxcvbnm",
  "qwertzuiopasdfghjklyxcvbnm",
  "0123456789",
];

function poolSize(pw: string): number {
  let pool = 0;
  if (/[a-z]/.test(pw)) pool += 26;
  if (/[A-Z]/.test(pw)) pool += 26;
  if (/[0-9]/.test(pw)) pool += 10;
  if (/[^a-zA-Z0-9]/.test(pw)) pool += 33; // rough printable-symbol count
  return pool;
}

function containsCommon(pw: string): string | null {
  const lower = pw.toLowerCase();
  for (const w of COMMON_WORDS) {
    if (lower.includes(w)) return w;
  }
  return null;
}

function repetitionPenalty(pw: string): number {
  // Repeating runs of 3+ identical chars or 2+ repeated substrings.
  let p = 0;
  if (/(.)\1{2,}/.test(pw)) p += 8;
  if (/(.{2,})\1+/.test(pw)) p += 6;
  return p;
}

function sequencePenalty(pw: string): number {
  const lower = pw.toLowerCase();
  for (const seq of SEQUENCES) {
    for (let len = 3; len <= 6; len++) {
      for (let i = 0; i + len <= seq.length; i++) {
        const slice = seq.slice(i, i + len);
        if (lower.includes(slice)) return 6 + len; // longer hit = more deducted
      }
    }
  }
  return 0;
}

export function estimateStrength(pw: string): PasswordStrength {
  if (!pw) {
    return { score: 0, entropy: 0, label: "Empty", feedback: ["Type a password to see strength."] };
  }
  const pool = poolSize(pw) || 1;
  // log2(pool^len) = len * log2(pool)
  let entropy = pw.length * Math.log2(pool);

  const feedback: string[] = [];

  if (pw.length < 10) {
    feedback.push("Use at least 12 characters.");
  }

  const common = containsCommon(pw);
  if (common) {
    entropy -= 14;
    feedback.push(`Contains common word "${common}".`);
  }

  const rp = repetitionPenalty(pw);
  if (rp > 0) {
    entropy -= rp;
    feedback.push("Avoid repeated characters or runs.");
  }

  const sp = sequencePenalty(pw);
  if (sp > 0) {
    entropy -= sp;
    feedback.push("Avoid keyboard / alphabet sequences.");
  }

  if (!/[A-Z]/.test(pw) || !/[a-z]/.test(pw)) feedback.push("Mix upper and lower case.");
  if (!/[0-9]/.test(pw)) feedback.push("Add a digit.");
  if (!/[^a-zA-Z0-9]/.test(pw)) feedback.push("Add a symbol.");

  // Clamp; very short passwords could go negative.
  entropy = Math.max(0, entropy);

  let score: 0 | 1 | 2 | 3 | 4;
  let label: string;
  if (entropy < 28) { score = 0; label = "Very weak"; }
  else if (entropy < 40) { score = 1; label = "Weak"; }
  else if (entropy < 52) { score = 2; label = "Fair"; }
  else if (entropy < 64) { score = 3; label = "Strong"; }
  else { score = 4; label = "Very strong"; }

  if (score >= 3 && feedback.length === 0) {
    feedback.push("Looks good.");
  }

  return { score, entropy, label, feedback };
}
