// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"sync"

	"github.com/folbricht/desync"
)

// packStore is the object-count-optimized S3 backend. It keeps desync's catar + CDC model,
// but groups compressed chunks into immutable pack objects instead of creating
// one object per chunk. This bounds object/inode growth per checkpoint and
// turns many small S3 requests into a handful of large requests while retaining
// incremental transfer for chunks referenced by the previous snapshot.
//
// The layout intentionally has no version directory:
//
//	<prefix>/beds/<bedID>/head.json
//	<prefix>/beds/<bedID>/snapshots/<snapshotID>.json
//	<prefix>/beds/<bedID>/packs/<first2>/<packID>.pack
//
// Packs remain per-bed for now. Store exposes one mutable snapshot per bed and
// no snapshot reference/fork primitive; making packs global before that model
// exists would make purge require unsafe cross-bed garbage collection.
type packStore struct {
	obj         objAPI
	prefix      string
	targetBytes int
}

const (
	packTargetBytes = 32 << 20
	packMetaBytes   = "bytes"
	packReadCache   = 2
)

type packHead struct {
	Snapshot   string `json:"snapshot"`
	Generation int64  `json:"generation"`
	Bytes      int64  `json:"bytes"`
}

type packManifest struct {
	Index  []byte         `json:"index"`
	Chunks []packChunkRef `json:"chunks"`
}

type packChunkRef struct {
	ID     string `json:"id"`
	Pack   string `json:"pack"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type packLocation struct {
	pack   string
	offset int64
	length int64
}

func newPack(ctx context.Context, cfg Config) (Store, error) {
	obj, err := newS3Obj(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newPackStore(obj, cfg.Prefix), nil
}

func newPackStore(obj objAPI, prefix string) *packStore {
	return &packStore{obj: obj, prefix: prefix, targetBytes: packTargetBytes}
}

func (s *packStore) Name() string { return "pack" }

func (s *packStore) bedPrefix(bedID string) string {
	return path.Join(s.prefix, "beds", bedID) + "/"
}

func (s *packStore) headKey(bedID string) string {
	return s.bedPrefix(bedID) + "head.json"
}

func (s *packStore) snapshotKey(bedID, snapshotID string) string {
	return s.bedPrefix(bedID) + "snapshots/" + snapshotID + ".json"
}

func (s *packStore) packPrefix(bedID string) string {
	return s.bedPrefix(bedID) + "packs/"
}

func (s *packStore) packKey(bedID, packID string) string {
	return s.packPrefix(bedID) + packID[:2] + "/" + packID + ".pack"
}

func (s *packStore) Stat(ctx context.Context, bedID string) (*SnapshotInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	defer cancel()
	meta, _, exists, err := s.obj.head(ctx, s.headKey(bedID))
	if err != nil || !exists {
		return nil, err
	}
	info := &SnapshotInfo{}
	if g, err := strconv.ParseInt(meta[generationMetaKey], 10, 64); err == nil {
		info.Generation = g
	}
	if b, err := strconv.ParseInt(meta[packMetaBytes], 10, 64); err == nil {
		info.Bytes = b
	}
	return info, nil
}

func (s *packStore) Persist(ctx context.Context, bedID, dir string, generation int64) error {
	prevMeta, _, prevExists, err := s.obj.head(ctx, s.headKey(bedID))
	if err != nil {
		return fmt.Errorf("store: persist %s: pre-write stat: %w", bedID, err)
	}
	if prevExists {
		if g, err := strconv.ParseInt(prevMeta[generationMetaKey], 10, 64); err == nil && g >= generation {
			return fmt.Errorf("store: persist %s: remote generation %d >= local %d: %w",
				bedID, g, generation, ErrConflict)
		}
	}

	var prevHead *packHead
	var prevManifest *packManifest
	if prevExists {
		head, err := s.loadHead(ctx, bedID)
		if err != nil {
			return err
		}
		manifest, err := s.loadManifest(ctx, bedID, head.Snapshot)
		if err != nil {
			return err
		}
		prevHead, prevManifest = &head, &manifest
	}

	known := make(map[desync.ChunkID]packLocation)
	if prevManifest != nil {
		known, err = manifestLocations(*prevManifest)
		if err != nil {
			return fmt.Errorf("store: persist %s: previous manifest: %w", bedID, err)
		}
	}
	w := newPackWriter(ctx, s, bedID, known)

	pr, pw := io.Pipe()
	go func() {
		src := &filteredFS{inner: desync.NewLocalFS(dir, desync.LocalFSOptions{}), root: dir}
		pw.CloseWithError(desync.Tar(ctx, pw, src))
	}()
	chunker, err := desync.NewChunker(pr, casChunkMin, casChunkAvg, casChunkMax)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("store: persist %s: chunker: %w", bedID, err)
	}
	// One worker preserves stream order inside newly created packs. Compression
	// is still streaming and a pack PUT amortizes many chunks/S3 operations.
	idx, err := desync.ChunkStream(ctx, chunker, w, 1)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("store: persist %s: pack chunks: %w", bedID, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("store: persist %s: flush pack: %w", bedID, err)
	}

	manifest, err := buildPackManifest(idx, w.locations)
	if err != nil {
		return fmt.Errorf("store: persist %s: build manifest: %w", bedID, err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("store: persist %s: encode manifest: %w", bedID, err)
	}
	snapshotID := objectID(manifestBytes)

	// Immutable data lands first; head.json is the only commit point. Readers
	// therefore see either the complete previous snapshot or the complete new one.
	if prevHead == nil || prevHead.Snapshot != snapshotID {
		putCtx, cancel := context.WithTimeout(ctx, s3OpTimeout)
		err = s.obj.put(putCtx, s.snapshotKey(bedID, snapshotID), bytes.NewReader(manifestBytes), int64(len(manifestBytes)), nil)
		cancel()
		if err != nil {
			return fmt.Errorf("store: persist %s: put manifest: %w", bedID, err)
		}
	}

	head := packHead{Snapshot: snapshotID, Generation: generation, Bytes: idx.Length()}
	headBytes, err := json.Marshal(head)
	if err != nil {
		return fmt.Errorf("store: persist %s: encode head: %w", bedID, err)
	}
	putCtx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	err = s.obj.put(putCtx, s.headKey(bedID), bytes.NewReader(headBytes), int64(len(headBytes)), map[string]string{
		generationMetaKey: strconv.FormatInt(generation, 10),
		packMetaBytes:     strconv.FormatInt(idx.Length(), 10),
	})
	cancel()
	if err != nil {
		return fmt.Errorf("store: persist %s: put head: %w", bedID, err)
	}
	return nil
}

func (s *packStore) Restore(ctx context.Context, bedID, dir string) error {
	head, err := s.loadHead(ctx, bedID)
	if err != nil {
		return err
	}
	manifest, err := s.loadManifest(ctx, bedID, head.Snapshot)
	if err != nil {
		return err
	}
	idx, err := desync.IndexFromReader(bytes.NewReader(manifest.Index))
	if err != nil {
		return fmt.Errorf("store: restore %s: decode index: %w", bedID, err)
	}
	locations, err := manifestLocations(manifest)
	if err != nil {
		return fmt.Errorf("store: restore %s: manifest: %w", bedID, err)
	}
	for _, chunk := range idx.Chunks {
		if _, ok := locations[chunk.ID]; !ok {
			return fmt.Errorf("store: restore %s: chunk %s has no pack location", bedID, chunk.ID.String())
		}
	}

	dst := desync.NewLocalFS(dir, desync.LocalFSOptions{NoSameOwner: true})
	r := newPackReader(ctx, s, bedID, locations)
	if err := desync.UnTarIndex(ctx, dst, idx, r, casConcurrency, desync.NullProgressBar{}); err != nil {
		return fmt.Errorf("store: restore %s: %w", bedID, err)
	}
	return nil
}

func (s *packStore) Delete(ctx context.Context, bedID string) error {
	keys, err := s.obj.list(ctx, s.bedPrefix(bedID))
	if err != nil {
		return fmt.Errorf("store: delete %s: list packs: %w", bedID, err)
	}
	if err := s.obj.del(ctx, keys); err != nil {
		return fmt.Errorf("store: delete %s: %w", bedID, err)
	}
	return nil
}

func (s *packStore) loadHead(ctx context.Context, bedID string) (packHead, error) {
	ctx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	defer cancel()
	rc, err := s.obj.get(ctx, s.headKey(bedID))
	if err != nil {
		return packHead{}, fmt.Errorf("store: head %s: %w", bedID, err)
	}
	defer rc.Close()
	var head packHead
	if err := json.NewDecoder(rc).Decode(&head); err != nil {
		return packHead{}, fmt.Errorf("store: decode head %s: %w", bedID, err)
	}
	if !validObjectID(head.Snapshot) {
		return packHead{}, fmt.Errorf("store: decode head %s: invalid snapshot id %q", bedID, head.Snapshot)
	}
	return head, nil
}

func (s *packStore) loadManifest(ctx context.Context, bedID, snapshotID string) (packManifest, error) {
	if !validObjectID(snapshotID) {
		return packManifest{}, fmt.Errorf("store: manifest %s: invalid snapshot id %q", bedID, snapshotID)
	}
	ctx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	defer cancel()
	rc, err := s.obj.get(ctx, s.snapshotKey(bedID, snapshotID))
	if err != nil {
		return packManifest{}, fmt.Errorf("store: manifest %s: %w", bedID, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return packManifest{}, fmt.Errorf("store: read manifest %s: %w", bedID, err)
	}
	if got := objectID(b); got != snapshotID {
		return packManifest{}, fmt.Errorf("store: manifest %s: digest %s, want %s", bedID, got, snapshotID)
	}
	var manifest packManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return packManifest{}, fmt.Errorf("store: decode manifest %s: %w", bedID, err)
	}
	return manifest, nil
}

func buildPackManifest(idx desync.Index, locations map[desync.ChunkID]packLocation) (packManifest, error) {
	var index bytes.Buffer
	if _, err := idx.WriteTo(&index); err != nil {
		return packManifest{}, err
	}
	manifest := packManifest{Index: index.Bytes(), Chunks: make([]packChunkRef, len(idx.Chunks))}
	for i, chunk := range idx.Chunks {
		location, ok := locations[chunk.ID]
		if !ok {
			return packManifest{}, fmt.Errorf("chunk %s has no pack location", chunk.ID.String())
		}
		manifest.Chunks[i] = packChunkRef{
			ID: chunk.ID.String(), Pack: location.pack,
			Offset: location.offset, Length: location.length,
		}
	}
	return manifest, nil
}

func manifestLocations(manifest packManifest) (map[desync.ChunkID]packLocation, error) {
	locations := make(map[desync.ChunkID]packLocation, len(manifest.Chunks))
	for _, ref := range manifest.Chunks {
		id, err := desync.ChunkIDFromString(ref.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid chunk id %q: %w", ref.ID, err)
		}
		if !validObjectID(ref.Pack) || ref.Offset < 0 || ref.Length <= 0 {
			return nil, fmt.Errorf("invalid location for chunk %s", ref.ID)
		}
		location := packLocation{pack: ref.Pack, offset: ref.Offset, length: ref.Length}
		if old, ok := locations[id]; ok && old != location {
			return nil, fmt.Errorf("chunk %s has conflicting pack locations", ref.ID)
		}
		locations[id] = location
	}
	return locations, nil
}

func objectID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func validObjectID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == sha256.Size
}

type pendingPackChunk struct {
	id     desync.ChunkID
	offset int64
	length int64
}

type packWriter struct {
	ctx       context.Context
	store     *packStore
	bedID     string
	target    int
	mu        sync.Mutex
	buf       bytes.Buffer
	pending   []pendingPackChunk
	locations map[desync.ChunkID]packLocation
}

func newPackWriter(ctx context.Context, store *packStore, bedID string, known map[desync.ChunkID]packLocation) *packWriter {
	target := store.targetBytes
	if target <= 0 {
		target = packTargetBytes
	}
	return &packWriter{ctx: ctx, store: store, bedID: bedID, target: target, locations: known}
}

func (w *packWriter) StoreChunk(chunk *desync.Chunk) error {
	data, err := chunk.Storage(casConverters)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.locations[chunk.ID()]; ok {
		return nil
	}
	if w.buf.Len() > 0 && w.buf.Len()+len(data) > w.target {
		if err := w.flushLocked(); err != nil {
			return err
		}
	}
	offset := int64(w.buf.Len())
	if _, err := w.buf.Write(data); err != nil {
		return err
	}
	w.pending = append(w.pending, pendingPackChunk{id: chunk.ID(), offset: offset, length: int64(len(data))})
	if w.buf.Len() >= w.target {
		return w.flushLocked()
	}
	return nil
}

func (w *packWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

func (w *packWriter) flushLocked() error {
	if w.buf.Len() == 0 {
		return nil
	}
	packID := objectID(w.buf.Bytes())
	ctx, cancel := context.WithTimeout(w.ctx, s3OpTimeout)
	err := w.store.obj.put(ctx, w.store.packKey(w.bedID, packID), bytes.NewReader(w.buf.Bytes()), int64(w.buf.Len()), nil)
	cancel()
	if err != nil {
		return err
	}
	for _, chunk := range w.pending {
		w.locations[chunk.id] = packLocation{pack: packID, offset: chunk.offset, length: chunk.length}
	}
	w.buf.Reset()
	w.pending = w.pending[:0]
	return nil
}

func (w *packWriter) GetChunk(desync.ChunkID) (*desync.Chunk, error) {
	return nil, fmt.Errorf("pack writer is write-only")
}

func (w *packWriter) HasChunk(id desync.ChunkID) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.locations[id]
	return ok, nil
}

func (w *packWriter) Close() error   { return nil }
func (w *packWriter) String() string { return "pack-writer:" + w.bedID }

type cachedPack struct {
	id   string
	data []byte
}

type packReader struct {
	ctx       context.Context
	store     *packStore
	bedID     string
	locations map[desync.ChunkID]packLocation
	mu        sync.Mutex
	cache     map[string]*list.Element
	lru       list.List
}

func newPackReader(ctx context.Context, store *packStore, bedID string, locations map[desync.ChunkID]packLocation) *packReader {
	return &packReader{
		ctx: ctx, store: store, bedID: bedID, locations: locations,
		cache: make(map[string]*list.Element),
	}
}

func (r *packReader) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	location, ok := r.locations[id]
	if !ok {
		return nil, fmt.Errorf("chunk %s has no pack location", id.String())
	}
	data, err := r.chunkStorage(location)
	if err != nil {
		return nil, err
	}
	// Chunk digest verification remains end-to-end even though the transfer
	// unit is now a pack rather than an individual object.
	return desync.NewChunkFromStorage(id, data, casConverters, false)
}

func (r *packReader) chunkStorage(location packLocation) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	element, ok := r.cache[location.pack]
	if ok {
		r.lru.MoveToFront(element)
	} else {
		ctx, cancel := context.WithTimeout(r.ctx, s3OpTimeout)
		rc, err := r.store.obj.get(ctx, r.store.packKey(r.bedID, location.pack))
		if err != nil {
			cancel()
			return nil, err
		}
		data, err := io.ReadAll(rc)
		closeErr := rc.Close()
		cancel()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if got := objectID(data); got != location.pack {
			return nil, fmt.Errorf("pack %s digest is %s", location.pack, got)
		}
		element = r.lru.PushFront(cachedPack{id: location.pack, data: data})
		r.cache[location.pack] = element
		if r.lru.Len() > packReadCache {
			oldest := r.lru.Back()
			delete(r.cache, oldest.Value.(cachedPack).id)
			r.lru.Remove(oldest)
		}
	}
	pack := element.Value.(cachedPack).data
	end := location.offset + location.length
	if location.offset < 0 || end < location.offset || end > int64(len(pack)) {
		return nil, fmt.Errorf("pack %s location [%d:%d] exceeds %d bytes", location.pack, location.offset, end, len(pack))
	}
	return pack[location.offset:end], nil
}

func (r *packReader) HasChunk(id desync.ChunkID) (bool, error) {
	_, ok := r.locations[id]
	return ok, nil
}

func (r *packReader) Close() error   { return nil }
func (r *packReader) String() string { return "pack-reader:" + r.bedID }
