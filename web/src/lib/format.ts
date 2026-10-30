const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });
const whole = new Intl.NumberFormat("en-US");

export const fmtCompact = (n: number) => compact.format(n);
export const fmtNumber = (n: number) => whole.format(Math.round(n));
export const fmtPercent = (n: number, digits = 1) => `${(n * 100).toFixed(digits)}%`;

export function fmtMoney(cents: number, currency: string) {
  return new Intl.NumberFormat("en-US", { style: "currency", currency, notation: "compact", maximumFractionDigits: 1 })
    .format(cents / 100);
}

/** "2026-10-29" -> "Oct 29" */
export function fmtDay(day: string) {
  const d = new Date(`${day}T00:00:00Z`);
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
}

/** "2026-10-30T08:21Z" -> "08:21" */
export const fmtMinute = (iso: string) => iso.slice(11, 16);

/** Round a max value up to a readable axis top (1, 2, 2.5, 5 x 10^n). */
export function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}
