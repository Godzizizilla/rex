package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"sync"

	"rex/internal/protocol"
)

const (
	historyOutputMax = 16 << 10
	historyMaxBytes  = 20 << 20 // compact the file beyond this size
	historyKeep      = 2000     // lines kept when compacting
)

// history persists finished requests as JSON lines in ~/.rex/history.jsonl.
type history struct {
	path string
	mu   sync.Mutex
}

// load returns the last limit requests.
func (h *history) load(limit int) ([]protocol.Request, error) {
	f, err := os.Open(h.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []protocol.Request
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var r protocol.Request
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		out = append(out, r)
		if len(out) > 2*limit {
			out = append(out[:0], out[len(out)-limit:]...)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, sc.Err()
}

// append writes a finished request.
func (h *history) append(r protocol.Request) {
	if len(r.Output) > historyOutputMax {
		r.Output = r.Output[len(r.Output)-historyOutputMax:]
	}
	if len(r.Diff) > historyOutputMax {
		r.Diff = r.Diff[:historyOutputMax]
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("history: %v", err)
		return
	}
	f.Write(append(line, '\n'))
	info, _ := f.Stat()
	f.Close()
	if info != nil && info.Size() > historyMaxBytes {
		h.compact()
	}
}

// compact keeps only the last historyKeep lines.
func (h *history) compact() {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) > historyKeep {
		lines = lines[len(lines)-historyKeep:]
	}
	tmp := h.path + ".tmp"
	if os.WriteFile(tmp, append(bytes.Join(lines, []byte("\n")), '\n'), 0600) == nil {
		os.Rename(tmp, h.path)
	}
}
