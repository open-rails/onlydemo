package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/open-rails/contentkit/access"
	"github.com/open-rails/contentkit/contentref"
	"github.com/open-rails/contentkit/media"
)

func isVideoType(t string) bool { return slices.Contains(videoTypes, t) }

// imageTeasers refuses a commit that makes a video a post's teaser: teasers
// are served to every viewer, and only images get the blurred derivation.
func (m *mediaService) imageTeasers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/commit" {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			writeUploadError(w, &media.UploadError{Code: media.CodeInvalid, Message: "unreadable body"})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body media.CommitBody
		if json.Unmarshal(raw, &body) != nil || body.Ref.Kind != kindPost {
			next.ServeHTTP(w, r)
			return
		}
		a, _ := r.Context().Value(mediaActorKey{}).(access.Actor)
		refusal, err := m.checkTeasers(r.Context(), a, m.ref(kindPost, body.Ref.ID), body.Ops)
		if err != nil {
			refusal = &media.UploadError{Code: "internal_error", Message: "internal error"}
		}
		if refusal != nil {
			writeUploadError(w, refusal)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkTeasers replays ops' teaser flags over the manifest. Callers
// ContentKit would refuse anyway pass through to it.
func (m *mediaService) checkTeasers(ctx context.Context, actor access.Actor, ref contentref.ContentRef, ops []media.Op) (*media.UploadError, error) {
	teasers := func(op media.Op) bool { t, _ := op.Meta["teaser"].(bool); return t }
	if !slices.ContainsFunc(ops, func(op media.Op) bool { return teasers(op) || op.Op == media.OpReplace }) {
		return nil, nil
	}
	if g, err := m.CanUpload(ctx, actor, ref); err != nil || !g.Allowed {
		return nil, err
	}
	item, err := m.kinds.Item(ref)
	if err != nil {
		return nil, nil
	}
	man, _, err := m.manifests.Get(ctx, ref)
	if errors.Is(err, media.ErrNotFound) {
		man = &media.Manifest{}
	} else if err != nil {
		return nil, err
	}
	teaser := map[string]bool{}
	types := map[string]string{} // original → content type
	for _, f := range man.Files {
		teaser[f.Name] = f.Teaser()
		types[f.Original] = f.Type
	}
	for _, op := range ops {
		if op.Op != media.OpInsert && op.Op != media.OpReplace {
			continue
		}
		t := teasers(op) || op.Op == media.OpReplace && op.Meta == nil && teaser[op.Name]
		if !t {
			continue
		}
		typ, ok := types[op.Original]
		if !ok {
			key, err := item.Original(op.Original)
			if err != nil {
				continue
			}
			obj, err := m.store.Head(ctx, key)
			if errors.Is(err, media.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
			typ = obj.ContentType
		}
		if !strings.HasPrefix(typ, "image/") {
			return &media.UploadError{Code: media.CodeInvalid, Message: "the teaser must be an image"}, nil
		}
	}
	return nil, nil
}

func writeUploadError(w http.ResponseWriter, e *media.UploadError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status())
	_ = json.NewEncoder(w).Encode(media.ErrorReply{Error: e.Message, Code: e.Code})
}
