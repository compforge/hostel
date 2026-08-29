//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestFilesystemAPIContract(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client
	const (
		bedID   = "filesystem-bed"
		root    = "/workspace/e2e-tree"
		initial = "/workspace/e2e-tree/nested/initial.txt"
		moved   = "/workspace/e2e-tree/result.txt"
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	created, err := c.json(ctx, "POST", "/directories", bedID, map[string]string{"path": root + "/nested"}, nil)
	cancel()
	if err != nil || created.Status != http.StatusOK {
		t.Fatalf("create directory: status=%d err=%v body=%s", created.Status, err, created.Body)
	}
	must2xx(t, "upload nested file", c.upload(t, bedID, initial, []byte("alpha\nbeta\nalpha\n")))

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var info map[string]fileInfoView
	infoResult, err := c.json(ctx, "GET", "/files/info?path="+url.QueryEscape(root)+"&path="+url.QueryEscape(initial), bedID, nil, &info)
	cancel()
	if err != nil || infoResult.Status != http.StatusOK {
		t.Fatalf("read file info: status=%d err=%v body=%s", infoResult.Status, err, infoResult.Body)
	}
	if info[root].Path != root || info[root].Type != "directory" || info[initial].Path != initial || info[initial].Type != "file" {
		t.Fatalf("file info did not preserve client paths: %+v", info)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var replaced map[string]struct {
		Count int `json:"replacedCount"`
	}
	replaceResult, err := c.json(ctx, "POST", "/files/replace", bedID, map[string]any{
		initial: map[string]string{"old": "alpha", "new": "omega"},
	}, &replaced)
	cancel()
	if err != nil || replaceResult.Status != http.StatusOK || replaced[initial].Count != 2 {
		t.Fatalf("replace file: status=%d err=%v result=%+v body=%s", replaceResult.Status, err, replaced, replaceResult.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	permissions, err := c.json(ctx, "POST", "/files/permissions", bedID, map[string]any{
		initial: map[string]any{"mode": 0o600},
	}, nil)
	cancel()
	if err != nil || permissions.Status != http.StatusOK {
		t.Fatalf("chmod file: status=%d err=%v body=%s", permissions.Status, err, permissions.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	rename, err := c.json(ctx, "POST", "/files/mv", bedID, []map[string]string{{"src": initial, "dest": moved}}, nil)
	cancel()
	if err != nil || rename.Status != http.StatusOK {
		t.Fatalf("rename file: status=%d err=%v body=%s", rename.Status, err, rename.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var movedInfo map[string]fileInfoView
	movedResult, err := c.json(ctx, "GET", "/files/info?path="+url.QueryEscape(moved), bedID, nil, &movedInfo)
	cancel()
	if err != nil || movedResult.Status != http.StatusOK || movedInfo[moved].Mode != 0o600 {
		t.Fatalf("moved file metadata: status=%d err=%v info=%+v body=%s", movedResult.Status, err, movedInfo, movedResult.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var found []fileInfoView
	search, err := c.json(ctx, "GET", "/files/search?path="+url.QueryEscape(root)+"&pattern="+url.QueryEscape("*.txt"), bedID, nil, &found)
	cancel()
	if err != nil || search.Status != http.StatusOK || len(found) != 1 || found[0].Path != moved {
		t.Fatalf("search renamed file: status=%d err=%v found=%+v body=%s", search.Status, err, found, search.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var listed []fileInfoView
	list, err := c.json(ctx, "GET", "/directories/list?path="+url.QueryEscape(root)+"&depth=2", bedID, nil, &listed)
	cancel()
	if err != nil || list.Status != http.StatusOK || !containsFile(listed, root+"/nested", "directory") || !containsFile(listed, moved, "file") {
		t.Fatalf("list directory tree: status=%d err=%v listed=%+v body=%s", list.Status, err, listed, list.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	line, err := c.request(ctx, "GET", "/files/download?path="+url.QueryEscape(moved)+"&offset=1&limit=1", bedID, nil, "")
	cancel()
	if err != nil || line.Status != http.StatusOK || string(line.Body) != "beta\n" {
		t.Fatalf("download line slice: status=%d err=%v body=%q", line.Status, err, line.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	deletedFile, err := c.json(ctx, "DELETE", "/files?path="+url.QueryEscape(moved), bedID, nil, nil)
	cancel()
	if err != nil || deletedFile.Status != http.StatusOK {
		t.Fatalf("delete file: status=%d err=%v body=%s", deletedFile.Status, err, deletedFile.Body)
	}
	if missing := c.download(t, bedID, moved); missing.Status != http.StatusNotFound {
		t.Fatalf("deleted file remains readable: status=%d body=%s", missing.Status, missing.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	deletedDir, err := c.json(ctx, "DELETE", "/directories?path="+url.QueryEscape(root), bedID, nil, nil)
	cancel()
	if err != nil || deletedDir.Status != http.StatusOK {
		t.Fatalf("delete directory: status=%d err=%v body=%s", deletedDir.Status, err, deletedDir.Body)
	}
}

func containsFile(files []fileInfoView, path, kind string) bool {
	for _, file := range files {
		if file.Path == path && file.Type == kind {
			return true
		}
	}
	return false
}
