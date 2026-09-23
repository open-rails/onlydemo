export function money(amount: string, currency = "USD") {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency,
  }).format(Number(amount) / 1_000_000);
}
export function date(value?: string | null) {
  return value
    ? new Date(value).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
      })
    : "—";
}
export function duration(hours?: number | null) {
  return hours
    ? hours % 24 === 0
      ? `every ${hours / 24} days`
      : `every ${hours} hours`
    : "one time";
}
export function micros(input: string) {
  if (!/^\d+(\.\d{1,2})?$/.test(input))
    throw new Error("Enter a positive amount with up to two decimal places.");
  const [whole, fraction = ""] = input.split(".");
  const value = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, "0"));
  if (value <= 0n) throw new Error("Price must be greater than zero.");
  return value.toString();
}
