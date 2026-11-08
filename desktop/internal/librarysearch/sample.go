package librarysearch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type sampleSelection struct {
	Name                        string
	Population, Selected        int64
	FirstPosition, LastPosition int64
	Deciles                     [10]int
	SourceQueueSHA256           string
}
type sampleProvenance struct {
	SourceWork, SourcePlanSHA256 string
	Seed                         int64
	RequestedPerCollection       int
	Collections                  []sampleSelection
}
type sampledLine struct {
	Position int64
	Line     []byte
}

// Algorithm R: a uniform fixed-size reservoir, with a deterministic per-collection
// seed. Retaining original order improves extraction locality without changing
// which records are selected.
func reservoirQueue(ctx context.Context, path string, q sourceQueue, k int, seed int64) ([]sampledLine, sampleSelection, error) {
	info := sampleSelection{Name: q.Name, Population: q.Records}
	if k < 1 || k > 100000 {
		return nil, info, errors.New("sample pages must be 1-100000 per collection")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, info, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return nil, info, e
	}
	if before.Size() != q.QueueBytes {
		return nil, info, errors.New("source queue size changed")
	}
	h := sha256.New()
	seedHash := sha256.Sum256([]byte(q.Name))
	rng := rand.New(rand.NewSource(seed ^ int64(binary.LittleEndian.Uint64(seedHash[:8]))))
	scan := bufio.NewScanner(io.TeeReader(f, h))
	scan.Buffer(make([]byte, 65536), 4<<20)
	rows := make([]sampledLine, 0, k)
	var count int64
	for scan.Scan() {
		if count%10000 == 0 {
			if e = ctx.Err(); e != nil {
				return nil, info, e
			}
		}
		count++
		slot := count - 1
		if count > int64(k) {
			slot = rng.Int63n(count)
		}
		if slot < int64(k) {
			row := sampledLine{count - 1, append([]byte(nil), scan.Bytes()...)}
			if count <= int64(k) {
				rows = append(rows, row)
			} else {
				rows[slot] = row
			}
		}
	}
	if e = scan.Err(); e != nil {
		return nil, info, e
	}
	after, e := f.Stat()
	if e != nil {
		return nil, info, e
	}
	if count != q.Records || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, info, errors.New("source queue population or identity changed")
	}
	if len(rows) == 0 {
		return nil, info, errors.New("source queue is empty")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	info.Selected = int64(len(rows))
	info.FirstPosition = rows[0].Position
	info.LastPosition = rows[len(rows)-1].Position
	info.SourceQueueSHA256 = hex.EncodeToString(h.Sum(nil))
	for _, row := range rows {
		bucket := row.Position * 10 / count
		if bucket > 9 {
			bucket = 9
		}
		info.Deciles[bucket]++
	}
	return rows, info, nil
}
func prepareSample(ctx context.Context, p expandPlan, out string, k int, seed int64) (result expandPlan, err error) {
	if out == "" {
		return result, errors.New("provide a new sample directory")
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return result, err
	}
	if k < 1 || k > 100000 {
		return result, errors.New("sample pages must be 1-100000")
	}
	if p.Version != 1 || len(p.Queues) == 0 {
		return result, errors.New("unsupported or empty source plan")
	}
	if err = p.Config.guard(); err != nil {
		return result, err
	}
	c := p.Config
	c.Work = out
	if err = c.guard(); err != nil {
		return result, err
	}
	if _, e := os.Lstat(out); !os.IsNotExist(e) {
		return result, errors.New("sample output must be new")
	}
	original, e := os.ReadFile(filepath.Join(p.Config.Work, "plan.json"))
	if e != nil {
		return result, e
	}
	hash := sha256.Sum256(original)
	provenance := &sampleProvenance{SourceWork: p.Config.Work, SourcePlanSHA256: hex.EncodeToString(hash[:]), Seed: seed, RequestedPerCollection: k}
	result = expandPlan{Version: 1, Created: time.Now().UTC().Format(time.RFC3339), Config: c, InputSHA256: p.InputSHA256, Sampling: provenance}
	temp, e := os.MkdirTemp(filepath.Dir(out), "."+filepath.Base(out)+"-partial-")
	if e != nil {
		return result, e
	}
	defer func() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "Incomplete sampling retained:", temp)
		}
	}()
	for _, name := range []string{"queues", "collections"} {
		if e = os.Mkdir(filepath.Join(temp, name), 0700); e != nil {
			return result, e
		}
	}
	for _, q := range p.Queues {
		if filepath.Base(q.Name) != q.Name || q.Name == "." || q.Name == ".." {
			return result, errors.New("invalid collection name")
		}
		if e = c.guard(); e != nil {
			return result, e
		}
		rows, info, e := reservoirQueue(ctx, filepath.Join(p.Config.Work, "queues", q.Name+".jsonl"), q, k, seed)
		if e != nil {
			return result, fmt.Errorf("sample %s: %w", q.Name, e)
		}
		path := filepath.Join(temp, "queues", q.Name+".jsonl")
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return result, e
		}
		w := bufio.NewWriterSize(f, 65536)
		var size int64
		for _, row := range rows {
			var d doc
			if e = json.Unmarshal(row.Line, &d); e != nil {
				f.Close()
				return result, e
			}
			if d.Collection != q.Name {
				f.Close()
				return result, errors.New("sample queue collection mismatch")
			}
			if e = validateCompactDoc(d); e != nil {
				f.Close()
				return result, e
			}
			if _, e = w.Write(row.Line); e != nil {
				f.Close()
				return result, e
			}
			if e = w.WriteByte('\n'); e != nil {
				f.Close()
				return result, e
			}
			size += int64(len(row.Line) + 1)
		}
		if e = w.Flush(); e != nil {
			f.Close()
			return result, e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return result, e
		}
		if e = f.Close(); e != nil {
			return result, e
		}
		q.Records = int64(len(rows))
		q.QueueBytes = size
		result.Queues = append(result.Queues, q)
		provenance.Collections = append(provenance.Collections, info)
		fmt.Fprintf(os.Stderr, "Sampled %s: %d/%d entries, source positions %d-%d\n", q.Name, info.Selected, info.Population, info.FirstPosition, info.LastPosition)
	}
	tempConfig := c
	tempConfig.Work = temp
	if e = writeExpandedCatalog(tempConfig, result.Queues); e != nil {
		return result, e
	}
	if e = atomicJSON(filepath.Join(temp, "plan.json"), result); e != nil {
		return result, e
	}
	if e = atomicJSON(filepath.Join(temp, "config.json"), c); e != nil {
		return result, e
	}
	if e = c.guard(); e != nil {
		return result, e
	}
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		return result, errors.New("sample output appeared during build")
	}
	if e = os.Rename(temp, out); e != nil {
		return result, e
	}
	return result, nil
}
func runSample(ctx context.Context, configPath, out string, k int, seed int64) error {
	b, e := os.ReadFile(configPath)
	if e != nil {
		return e
	}
	var c expandConfig
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	c, e = normalizeConfig(c)
	if e != nil {
		return e
	}
	b, e = os.ReadFile(filepath.Join(c.Work, "plan.json"))
	if e != nil {
		return e
	}
	var p expandPlan
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	if p.Config.Work != c.Work || p.Config.VolumeUUID != c.VolumeUUID {
		return errors.New("sample source plan differs from configuration")
	}
	p.Config = c
	result, e := prepareSample(ctx, p, out, k, seed)
	if e != nil {
		return e
	}
	var count int64
	for _, q := range result.Queues {
		count += q.Records
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"collections": len(result.Queues), "sample_pages": count, "config": filepath.Join(result.Config.Work, "config.json"), "seed": seed})
}
