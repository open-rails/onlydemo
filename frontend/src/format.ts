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
export function dateTime(value?: string | null) {
  return value
    ? new Date(value).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
        hour: "numeric",
        minute: "2-digit",
      })
    : "—";
}
export function duration(hours?: number | null) {
  if (!hours) return "one time";
  if (hours === 1) return "every hour";
  if (hours === 24) return "every day";
  return hours % 24 === 0
    ? `every ${hours / 24} days`
    : `every ${hours} hours`;
}
// A price as ContentKit's offers take it: a positive decimal, "4.99".
export function amount(input: string, minimum = 0) {
  if (!/^\d+(\.\d{1,2})?$/.test(input))
    throw new Error("Enter a positive amount with up to two decimal places.");
  const value = Number(input);
  if (value <= 0) throw new Error("Price must be greater than zero.");
  if (value < minimum) throw new Error(`The price is at least $${minimum.toFixed(2)}.`);
  return value.toFixed(2);
}
