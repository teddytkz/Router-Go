// Package sysstat samples CPU and memory use of this process (and only this process) for the dashboard.
package sysstat

import (
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	interval = 2 * time.Second
	history  = 60 // samples kept: two minutes
)

// Sample is one reading of Router-Go itself. CPU is percent of the whole machine's capacity (0-100), RSS is bytes.
type Sample struct {
	T   time.Time `json:"t"`
	CPU float64   `json:"cpu"`
	RSS uint64    `json:"rss"`
}

// Snapshot is the latest reading plus the recent history (oldest first).
type Snapshot struct {
	Current    Sample   `json:"current"`
	CPUs       int      `json:"cpus"`
	TotalRAM   uint64   `json:"totalRam"` // bytes of physical RAM on the machine, 0 if unknown
	GoHeap     uint64   `json:"goHeap"` // bytes the Go runtime has allocated
	Goroutines int      `json:"goroutines"`
	History    []Sample `json:"history"`
}

type Sampler struct {
	once sync.Once
	mu   sync.Mutex
	proc *process.Process
	hist []Sample
}

// Default is started lazily on the first Snapshot call, so nothing runs for a gateway nobody looks at.
var Default = &Sampler{}

func (s *Sampler) start() {
	s.once.Do(func() {
		s.proc, _ = process.NewProcess(int32(os.Getpid()))
		if s.proc != nil {
			_, _ = s.proc.Percent(0) // gopsutil reports usage since the previous call: prime it
		}
		s.sample()
		go func() {
			for range time.Tick(interval) {
				s.sample()
			}
		}()
	})
}

func (s *Sampler) sample() {
	cur := Sample{T: time.Now()}
	if s.proc != nil {
		if p, err := s.proc.Percent(0); err == nil { // percent of ONE core, may exceed 100
			cur.CPU = min(max(p/float64(runtime.NumCPU()), 0), 100)
		}
		if m, err := s.proc.MemoryInfo(); err == nil {
			cur.RSS = m.RSS
		}
	}
	s.mu.Lock()
	s.hist = append(s.hist, cur)
	if len(s.hist) > history {
		s.hist = s.hist[len(s.hist)-history:]
	}
	s.mu.Unlock()
}

func (s *Sampler) Snapshot() Snapshot {
	s.start()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.mu.Lock()
	defer s.mu.Unlock()
	h := append([]Sample{}, s.hist...)
	var total uint64
	if v, err := mem.VirtualMemory(); err == nil {
		total = v.Total
	}
	return Snapshot{Current: h[len(h)-1], CPUs: runtime.NumCPU(), TotalRAM: total, GoHeap: ms.HeapAlloc, Goroutines: runtime.NumGoroutine(), History: h}
}
