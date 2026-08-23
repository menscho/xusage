package fixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xusage/internal/model"
	"xusage/internal/sources"
)

// TestWriteCorpusDeterministic checks same-seed corpora are byte-identical.
func TestWriteCorpusDeterministic(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	opts := CorpusOpts{WithErrors: true}
	infoA, err := WriteCorpus(dirA, opts)
	if err != nil {
		t.Fatal(err)
	}
	infoB, err := WriteCorpus(dirB, opts)
	if err != nil {
		t.Fatal(err)
	}
	if infoA.Files != infoB.Files || infoA.Entries != infoB.Entries {
		t.Fatalf("corpus summaries differ: %+v vs %+v", infoA, infoB)
	}
	err = filepath.WalkDir(dirA, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dirA, path)
		a, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(dirB, rel))
		if err != nil {
			return err
		}
		if string(a) != string(b) {
			t.Errorf("file %s differs between runs", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestWriteCorpusDifferentSeeds differs: distinct seeds must not coincide.
func TestWriteCorpusDifferentSeeds(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	if _, err := WriteCorpus(dirA, CorpusOpts{Seed: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCorpus(dirB, CorpusOpts{Seed: 2}); err != nil {
		t.Fatal(err)
	}
	a := readAll(t, dirA)
	b := readAll(t, dirB)
	same := len(a) == len(b)
	for rel, data := range a {
		if b[rel] != data {
			same = false
		}
	}
	if same {
		t.Error("different seeds produced identical corpora")
	}
}

// TestWriteCorpusLayout checks the expected tree shape, per-day files, and
// provider list.
func TestWriteCorpusLayout(t *testing.T) {
	dir := t.TempDir()
	info, err := WriteCorpus(dir, CorpusOpts{Projects: 2, Sessions: 1, Days: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		filepath.Join("claude", "projects", "proj-00", "proj-00-sess-00-d01.jsonl"),
		filepath.Join("claude", "projects", "proj-00", "proj-00-sess-00-d00.jsonl"),
		filepath.Join("claude", "projects", "proj-01", "proj-01-sess-00-d01.jsonl"),
		filepath.Join("codex", "sessions", "codex-sess-00.jsonl"),
		filepath.Join("codex", "archived_sessions", "codex-sess-01.jsonl"),
	} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s in corpus: %v", want, err)
		}
	}
	wantFiles := 2*1*2 + 2 // claude projects×sessions×days + codex live+archived
	if info.Files != wantFiles {
		t.Errorf("got %d files, want %d", info.Files, wantFiles)
	}
	found := map[string]bool{}
	for _, p := range info.Providers {
		found[p] = true
	}
	if !found["claude"] || !found["codex"] {
		t.Errorf("expected claude+codex providers, got %v", info.Providers)
	}
	if info.Entries == 0 {
		t.Error("corpus reported zero entries")
	}
}

// TestCorpusLinesAreJSON feeds every corpus line through a Source-style parse
// stub: every line except the deliberately truncated ones must decode as a
// JSON object.
func TestCorpusLinesAreJSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteCorpus(dir, CorpusOpts{WithErrors: true}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var v map[string]any
			if err := json.Unmarshal([]byte(line), &v); err != nil &&
				!strings.HasPrefix(line, `{"message":{"id":"msg_trunc"`) {
				t.Errorf("%s line %d is not valid JSON: %v\n%s", filepath.Base(path), i+1, err, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCorpusParsesThroughSourceStub runs a minimal sources.Source over the
// corpus tree: discovery finds every .jsonl file, parsing yields entries
// without error, and the parsed count matches CorpusInfo.Entries exactly.
func TestCorpusParsesThroughSourceStub(t *testing.T) {
	dir := t.TempDir()
	info, err := WriteCorpus(dir, CorpusOpts{Projects: 2, Days: 3, WithErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	// Entry-counting stand-in for a real adapter Parse func: Claude-shape
	// files contribute their usage-block lines, Codex rollouts their
	// token_count events — matching the CorpusInfo.Entries contract.
	countEntries := func(path string) ([]model.Entry, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var v map[string]any
			if json.Unmarshal([]byte(line), &v) != nil {
				continue // malformed noise
			}
			if strings.Contains(path, "codex") {
				if strings.Contains(line, `"token_count"`) {
					n++
				}
				continue
			}
			if strings.Contains(line, `"input_tokens"`) {
				n++
			}
		}
		return make([]model.Entry, n), nil
	}
	src := sources.Source{
		Name:     "stub",
		Discover: func() ([]string, error) { return walkJSONL(dir) },
		Parse:    countEntries,
	}
	paths, err := src.Discover()
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if len(paths) != info.Files {
		t.Fatalf("discovered %d files, corpus reported %d", len(paths), info.Files)
	}
	total := 0
	for _, p := range paths {
		entries, err := src.Parse(p)
		if err != nil {
			t.Fatalf("parse %s failed: %v", p, err)
		}
		total += len(entries)
	}
	if total != info.Entries {
		t.Errorf("stub parsed %d entries, corpus reported %d", total, info.Entries)
	}
}

func readAll(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// walkJSONL lists every .jsonl file under root, sorted for determinism.
func walkJSONL(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}
