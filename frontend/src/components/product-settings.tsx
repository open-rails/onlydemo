import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Product } from "@openrails/billing-ui/client";
import { request } from "../api";
import { ErrorState, FormError, Loading } from "./states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Field, FieldLabel } from "./ui/field";
import { Input } from "./ui/input";
import { Button } from "./ui/button";

export function ProductSettings({ kind, id, onUpdated }: { kind: "entities" | "contents"; id: string; onUpdated?: () => void }) {
  const key = `onlydemo:${kind === "entities" ? "group" : "content"}:${id}`;
  const read = useQuery({ queryKey: ["product", key], queryFn: () => request<Product | null>(`/api/v1/products?key=${encodeURIComponent(key)}`) });
  if (read.isPending) return <Loading />;
  if (read.error) return <ErrorState error={read.error} retry={() => void read.refetch()} />;
  return <ProductForm key={`${id}:${read.data?.revision}:${read.data?.prices[0]?.id}`} kind={kind} id={id} product={read.data ?? null} onUpdated={onUpdated} />;
}
function ProductForm({ kind, id, product, onUpdated }: { kind: "entities" | "contents"; id: string; product: Product | null; onUpdated?: () => void }) {
  const client = useQueryClient();
  const price = product?.prices[0];
  const [amount, setAmount] = useState(price ? String(Number(price.unit_amount) / 1_000_000) : "0");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const membership = kind === "entities";
  const save = async (event?: FormEvent) => {
    event?.preventDefault();
    setBusy(true); setError("");
    try {
      await request(`/api/v1/products/${kind}/${id}`, event ? { method: "PUT", body: JSON.stringify({ amount, renew_hours: membership && Number(amount) > 0 ? 720 : 0 }) } : { method: "DELETE" });
      await client.invalidateQueries(); onUpdated?.();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Could not save price."); }
    finally { setBusy(false); }
  };
  return <Card className="my-4">
    <CardHeader><CardTitle>{membership ? "Channel membership" : "Post purchase"}</CardTitle><CardDescription>
      {membership ? "Set 0 for free membership, or a monthly price of at least $1. Existing subscriptions keep their accepted terms." : "Set the one-time price, at least $0.50. Choose the post’s access rule in the editor."}
    </CardDescription></CardHeader>
    <CardContent><form className="flex flex-col gap-4" onSubmit={(event) => void save(event)}>
      <Field><FieldLabel htmlFor={`price-${id}`}>{membership ? "USD every 30 days" : "USD"}</FieldLabel><Input id={`price-${id}`} required inputMode="decimal" pattern="[0-9]+(\.[0-9]{1,2})?" value={amount} onChange={(event) => setAmount(event.target.value)} /></Field>
      <FormError>{error}</FormError><div className="flex gap-2"><Button disabled={busy}>Save price</Button>{product && !product.archived && <Button type="button" variant="outline" disabled={busy} onClick={() => void save()}>Stop new purchases</Button>}</div>
    </form></CardContent>
  </Card>;
}
