import { describe, expect, it } from "vitest";
import { channelPath, contentURLs, parseCode, postLink, postPath } from "./paths";

// A post as the API returns it: the server owns the code and the slug.
const post = { code: "G4VRQ3ZQ5", slug: "night-before-the-counteroffensive" };

describe("post links", () => {
  it("are /en/p/{CODE}/{slug} from the API's code and slug", () => {
    expect(postPath(post)).toBe("/en/p/G4VRQ3ZQ5/night-before-the-counteroffensive");
    expect(postPath({ code: "G4VRQ3ZQ5", slug: "" })).toBe("/en/p/G4VRQ3ZQ5");
  });

  it("read any spelling of the code from the route", () => {
    expect(parseCode("g4vr-q3zq5")).toBe("G4VRQ3ZQ5");
    expect(parseCode("346791971")).toBeNull(); // a legacy numeric id is never a code
    expect(parseCode("not-a-code")).toBeNull();
  });

  it("replace a stale address with the canonical one, keeping the query", () => {
    expect(contentURLs.canonical("/p/g4vrq3zq5/old-title?ref=feed", postLink(post))).toEqual({
      path: "/en/p/G4VRQ3ZQ5/night-before-the-counteroffensive",
      location: "/en/p/G4VRQ3ZQ5/night-before-the-counteroffensive?ref=feed",
      redirect: true,
    });
    expect(contentURLs.canonical(postPath(post), postLink(post))?.redirect).toBe(false);
    expect(contentURLs.canonical("/c/lunarose", postLink(post))).toBeNull();
  });
});

describe("channel links", () => {
  it("use the API's canonical name slug instead of rebuilding from its handle", () => {
    const channel = { slug: "lunarose", path: "/en/c/H7KMN2PQ8/luna-rose" };
    expect(channelPath(channel)).toBe("/en/c/H7KMN2PQ8/luna-rose");
  });

  it("redirect an unprefixed or stale group address through the shared URL helpers", () => {
    expect(contentURLs.canonical("/c/h7kmn2pq8/old-name?ref=feed", {
      content_kind: "channel", code: "H7KMN2PQ8", slug: "luna-rose",
    })).toEqual({
      path: "/en/c/H7KMN2PQ8/luna-rose",
      location: "/en/c/H7KMN2PQ8/luna-rose?ref=feed",
      redirect: true,
    });
  });
});
