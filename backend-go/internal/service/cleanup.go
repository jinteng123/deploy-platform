package service

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CleanupService deletes files older than maxAge from the transfer dir once a
// day at 02:00 Asia/Shanghai, mirroring the Python APScheduler job.
type CleanupService struct {
	dir       string
	maxAge    time.Duration
	loc       *time.Location
	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewCleanupService(dir string, maxAgeDays int) *CleanupService {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		log.Printf("[cleanup] Asia/Shanghai 时区加载失败，使用本地时区: %v", err)
		loc = time.Local
	}
	return &CleanupService{
		dir:    dir,
		maxAge: time.Duration(maxAgeDays) * 24 * time.Hour,
		loc:    loc,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (s *CleanupService) Start() {
	s.startOnce.Do(func() {
		go func() {
			defer close(s.done)
			for {
				timer := time.NewTimer(s.nextDelay())
				select {
				case <-s.stop:
					timer.Stop()
					return
				case <-timer.C:
					s.runOnce()
				}
			}
		}()
	})
}

// Stop halts the scheduler and waits for the goroutine to exit. It is safe to
// call multiple times, including without a prior Start.
func (s *CleanupService) Stop() {
	s.Start()
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
}

// nextDelay returns the duration until the next 02:00 in the configured zone.
func (s *CleanupService) nextDelay() time.Duration {
	now := time.Now().In(s.loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, s.loc)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return time.Until(next)
}

// runOnce removes regular files and symlinks whose mtime is older than
// maxAge. A missing directory is skipped.
func (s *CleanupService) runOnce() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[cleanup] 目录不存在，跳过: %s", s.dir)
		} else {
			log.Printf("[cleanup] 读取目录失败 %s: %v", s.dir, err)
		}
		return
	}
	now := time.Now()
	removed, failed := 0, 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			log.Printf("[cleanup] stat 失败 %s: %v", e.Name(), err)
			failed++
			continue
		}
		if !info.Mode().IsRegular() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		if now.Sub(info.ModTime()) <= s.maxAge {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		if err := os.Remove(path); err != nil {
			log.Printf("[cleanup] 删除失败 %s: %v", path, err)
			failed++
			continue
		}
		removed++
	}
	log.Printf("[cleanup] 清理完成: 删除 %d 个文件, 失败 %d 个", removed, failed)
}
