// Package cgroup은 cgroup v2로 컨테이너의 자원을 제한하고 사용량을 읽는다.
package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const root = "/sys/fs/cgroup/minibox"

// Limits에서 0은 "제한 없음"이다.
type Limits struct {
	Mem  int64   // 바이트
	CPU  float64 // 코어 수
	Pids int
}

// Create는 root/<id> cgroup을 만들고 제한을 쓴다. 반환한 경로에 프로세스를 넣으면 된다.
func Create(id string, l Limits) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// 하위 cgroup에서 컨트롤러를 쓰려면 부모의 subtree_control에서 켜야 한다
	if err := write(root, "cgroup.subtree_control", "+cpu +memory +pids"); err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	var err error
	set := func(file, val string) {
		if err == nil {
			err = write(dir, file, val)
		}
	}
	if l.Mem > 0 {
		set("memory.max", strconv.FormatInt(l.Mem, 10))
		// swap으로 빠지면 OOM이 안 나고 느려지기만 한다
		set("memory.swap.max", "0")
	}
	if l.CPU > 0 {
		// 100ms 주기마다 CPU*100ms만큼 쓸 수 있다
		set("cpu.max", fmt.Sprintf("%d 100000", int(l.CPU*100000)))
	}
	if l.Pids > 0 {
		set("pids.max", strconv.Itoa(l.Pids))
	}
	if err != nil {
		os.Remove(dir)
		return "", err
	}
	return dir, nil
}

// Summary는 실험 결과로 쓸 사용량 요약 한 줄을 만든다.
func Summary(dir string, elapsed time.Duration) string {
	usec := float64(keyed(dir, "cpu.stat", "usage_usec"))
	return fmt.Sprintf("실행 %.1fs | CPU 평균 %.2f코어 | 메모리 최대 %.1fMiB | oom_kill %d | 프로세스 최대 %d (pids.max에 막힌 fork %d회)",
		elapsed.Seconds(),
		usec/float64(elapsed.Microseconds()),
		float64(single(dir, "memory.peak"))/(1<<20),
		keyed(dir, "memory.events", "oom_kill"),
		single(dir, "pids.peak"),
		keyed(dir, "pids.events", "max"))
}

// Remove는 cgroup을 지운다. 컨테이너의 PID 1이 끝나면 커널이 나머지 프로세스를
// 죽이지만 조금 걸리므로, 비어 있을 때까지 잠깐 기다린다.
func Remove(dir string) error {
	write(dir, "cgroup.kill", "1")
	var err error
	for i := 0; i < 50; i++ {
		if err = os.Remove(dir); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

// ParseSize는 "64m", "1g", "512k", "1000" 같은 값을 바이트로 바꾼다.
func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("빈 크기")
	}
	mult := int64(1)
	switch strings.ToLower(s[len(s)-1:]) {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("잘못된 크기: %q", s)
	}
	return n * mult, nil
}

func write(dir, file, val string) error {
	return os.WriteFile(filepath.Join(dir, file), []byte(val), 0)
}

// single은 숫자 하나만 들어 있는 파일(memory.peak 등)을 읽는다.
func single(dir, file string) int64 {
	b, _ := os.ReadFile(filepath.Join(dir, file))
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// keyed는 "키 값" 줄로 된 파일(cpu.stat, memory.events 등)에서 값 하나를 읽는다.
func keyed(dir, file, key string) int64 {
	b, _ := os.ReadFile(filepath.Join(dir, file))
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == key {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
}
