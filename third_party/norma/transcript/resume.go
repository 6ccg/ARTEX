package transcript

import (
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/Autumn-27/norma/llm"
)

const (
	minCachedTranscript = 8 << 20
	maxCachedTranscript = 64 << 20
	maxCachedRecords    = 20_000
)

type resumeFile struct {
	path    string
	info    os.FileInfo
	offset  int64
	records []Record
}

// Keep one process-wide slot so the cache does not grow with the number of
// conversations or with the number of Store instances pointing at the same tree.
var recentResume struct {
	sync.Mutex
	file resumeFile
}

func cachedTranscript(path string) resumeFile {
	recentResume.Lock()
	defer recentResume.Unlock()
	if recentResume.file.path == path {
		return recentResume.file
	}
	return resumeFile{}
}

func sameFileVersion(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

// Only appends performed by Store.Append certify that the cached prefix is
// still intact. A different identity or an unexpected write forces a reload.
func noteTranscriptAppend(path string, before, after os.FileInfo, written int64) {
	recentResume.Lock()
	defer recentResume.Unlock()
	old := &recentResume.file
	if old.path != path {
		return
	}
	if after != nil && after.Size() <= maxCachedTranscript && after.Size() == before.Size()+written &&
		sameFileVersion(old.info, before) && os.SameFile(before, after) {
		old.info = after
	} else {
		*old = resumeFile{}
	}
}

// ResumeHistory rebuilds the complete session and usage from the JSONL log,
// while reusing the last large transcript's verified prefix across appends.
func (s *Store) ResumeHistory(sessionID string) ([]llm.Message, llm.Usage, string, error) {
	path := s.MainPath(sessionID)
	records, shared, err := resumeRecords(path)
	if err != nil {
		return nil, llm.Usage{}, "", err
	}
	msgs, usage := Messages(records)
	if shared {
		for i := range msgs {
			// Strings may be shared, while message slices and RawMessages remain
			// private to the caller so session edits cannot change the cache.
			msgs[i].Content = cloneBlocks(msgs[i].Content)
		}
	}
	return msgs, usage, LastUUID(records), nil
}

func cloneBlocks(blocks []llm.ContentBlock) []llm.ContentBlock {
	cloned := slices.Clone(blocks)
	for i := range cloned {
		cloned[i].Input = slices.Clone(cloned[i].Input)
		cloned[i].Content = cloneBlocks(cloned[i].Content)
	}
	return cloned
}

func resumeRecords(path string) ([]Record, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.Size() < minCachedTranscript || info.Size() > maxCachedTranscript {
		records, err := loadRecords(f)
		return records, false, err
	}
	var prefix []Record
	cached := cachedTranscript(path)
	if sameFileVersion(info, cached.info) {
		if cached.offset == info.Size() {
			return cached.records, true, nil
		}
		if _, err := f.Seek(cached.offset, 0); err != nil {
			return nil, false, err
		}
		prefix = cached.records
	}
	tail, err := loadRecords(f)
	if err != nil {
		return nil, false, err
	}
	records := tail
	if len(prefix) > 0 {
		records = append(slices.Clip(prefix), tail...)
	}
	after, err := f.Stat()
	var lastByte [1]byte
	_, tailErr := f.ReadAt(lastByte[:], info.Size()-1)
	if len(records) <= maxCachedRecords && err == nil && sameFileVersion(info, after) && tailErr == nil && lastByte[0] == '\n' {
		recentResume.Lock()
		recentResume.file = resumeFile{path: path, info: info, offset: info.Size(), records: records}
		recentResume.Unlock()
		return records, true, nil
	}
	return records, len(prefix) > 0, nil
}
