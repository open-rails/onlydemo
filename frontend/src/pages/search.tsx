import { useNavigate, useSearchParams } from "react-router-dom";
import type { Card, EntityCard } from "@openrails/contentkit-ui/client";
import { SearchBox, SearchResults, ChannelDirectory } from "@openrails/contentkit-ui";
import { Search01Icon } from "@hugeicons/core-free-icons";
import { EmptyState } from "../components/states";
import { Post } from "./explore";
import { channelPath, postPath, searchPath } from "../paths";

// ContentKit supplies canonical paths for both posts and metadata.
const href = (item: Card | EntityCard) => ("title" in item ? postPath({ code: item.code, slug: "", path: item.path }) : channelPath(item));

/** Posts and channels as the reader types, recent searches while empty; Enter opens the results. */
export function SiteSearch({ className }: { className?: string }) {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  return (
    <SearchBox
      key={params.get("q") ?? ""}
      className={className}
      kind="post"
      entityKinds={[]}
      channels
      channelHref={channelPath}
      defaultValue={params.get("q") ?? ""}
      placeholder="Search posts and creators"
      href={href}
      onSearch={(q) => navigate(searchPath(q))}
    />
  );
}

export function SearchPage() {
  const q = (useSearchParams()[0].get("q") ?? "").trim();
  return (
    <>
      <div className="page-title">
        <h1>Search</h1>
      </div>
      <SiteSearch className="mb-4" />
      {q ? (
        <>
        <ChannelDirectory query={{ q, limit: 12 }} href={channelPath} />
        <SearchResults
          className="feed"
          surface="search"
          query={{ q, kind: "post" }}
          renderCard={(card) => <Post card={card} />}
          empty={
            <EmptyState icon={Search01Icon} title="No posts match.">
              Try other words, or a creator's name.
            </EmptyState>
          }
        />
        </>
      ) : (
        <EmptyState icon={Search01Icon} title="Search posts and creators." />
      )}
    </>
  );
}
